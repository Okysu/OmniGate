package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"omnigate/internal/channel"
	"omnigate/internal/platform/netguard"
	"omnigate/internal/plugin"
	"omnigate/internal/plugin/engine"
	"omnigate/internal/protocol"
	"omnigate/internal/requestlog"
	"omnigate/internal/routing"
	"omnigate/internal/subscription"
)

// Custom-protocol channels (phase9-api.md §2): the channel's plugin speaks the
// upstream protocol. The client request is converted to Chat Completions (the
// pivot), buildRequest (+ signRequest) turns it into the upstream HTTP
// request, and parseResponse / parseStream (+ endStream) turn the answer back
// into a Chat Completions response or CanonicalEvents, which are converted to
// the client protocol. All hooks of one request run in one pinned runtime
// with a 5 s JS budget; any plugin failure is a plugin_error (retried on
// another channel while nothing reached the client, like a server error).

// hopHeaders may not be set by a plugin (the transport manages them).
var hopHeaders = map[string]bool{
	"host": true, "content-length": true, "connection": true, "transfer-encoding": true, "te": true, "trailer": true,
	"upgrade": true, "keep-alive": true, "proxy-authorization": true, "proxy-connection": true, "accept-encoding": true,
}

// pluginError is the plugin_error for a failed hook.
func pluginError(err error) *protocol.GatewayError {
	msg := err.Error()
	var pe *engine.Error
	if errors.As(err, &pe) {
		msg = pe.Message
	}
	return protocol.NewError(protocol.ErrPluginError, "channel plugin failed: "+protocol.Redact(msg))
}

// canonicalFailure classifies an upstream error reported by the plugin (an
// error event or a CanonicalResponse error; status 0 = 502).
func canonicalFailure(ce *protocol.CanonicalError) (*protocol.GatewayError, string, bool) {
	status := ce.Status
	if status < 400 || status > 599 {
		status = http.StatusBadGateway
	}
	return classifyStatus(status, "upstream: "+protocol.Redact(truncateMsg(ce.Message)))
}

func truncateMsg(s string) string {
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}

// attemptCustom is attempt for a channel whose plugin implements the protocol.
func (g *Gateway) attemptCustom(w http.ResponseWriter, r *http.Request, st *reqState, rt *channel.Runtime, tier channel.Tier) (*protocol.GatewayError, string) {
	upstreamModel, _ := rt.UpstreamModel(st.info.Model)
	att := requestlog.Attempt{ChannelID: rt.ID, ChannelName: rt.Name}
	attStart := time.Now()
	st.entry.ChannelID, st.entry.ChannelName, st.entry.UpstreamModel = &rt.ID, &rt.Name, &upstreamModel
	st.entry.ChannelTier = string(tier)
	st.entry.ServedModel = st.info.Model
	defer func() {
		att.DurationMs = time.Since(attStart).Milliseconds()
		st.entry.Attempts = append(st.entry.Attempts, att)
	}()
	fail := func(e *protocol.GatewayError, class string, health bool) (*protocol.GatewayError, string) {
		c := e.Class
		att.ErrorClass = &c
		if att.StatusCode == 0 {
			att.StatusCode = e.Status
		}
		if health {
			g.reg.Breaker.Failure(rt.ID, e.Message)
		} else {
			g.reg.Breaker.Release(rt.ID)
		}
		return e, class
	}
	pluginFail := func(err error) (*protocol.GatewayError, string) {
		g.log.WarnContext(r.Context(), "custom protocol plugin failed", "channel_id", rt.ID, "plugin", rt.Plugin.Key, "err", err)
		return fail(pluginError(err), routing.RetryServerError, true)
	}

	// Client request → Chat Completions (CanonicalRequest).
	var chatBody []byte
	var err error
	if st.dialect == protocol.OpenAIChat {
		chatBody, err = protocol.RewriteForPassthrough(protocol.OpenAIChat, st.body, upstreamModel, st.info.Stream)
	} else {
		chatBody, st.warnings, err = protocol.ConvertRequest(st.dialect, protocol.OpenAIChat, st.body, upstreamModel, "max_tokens", st.compat)
	}
	if err != nil {
		g.reg.Breaker.Release(rt.ID)
		return convertError(err), ""
	}

	ctx, cancel := context.WithCancelCause(r.Context())
	defer cancel(nil)
	ps, err := rt.Plugin.NewProtocolSession(ctx, rt.PluginEnv(g.reg.HTTPFor(rt), g.opts.UserAgent))
	if err != nil {
		return pluginFail(err)
	}
	defer ps.Close()
	spec, _, err := ps.BuildRequest(ctx, chatBody)
	if err != nil {
		return pluginFail(err)
	}
	var body io.Reader
	if spec.Body != nil {
		body = bytes.NewReader(spec.Body)
	}
	req, err := http.NewRequestWithContext(ctx, spec.Method, spec.URL.String(), body)
	if err != nil {
		return pluginFail(err)
	}
	for k, v := range rt.Config.Headers {
		req.Header.Set(k, v)
	}
	for k, v := range spec.Headers {
		if !hopHeaders[strings.ToLower(k)] {
			req.Header.Set(k, v)
		}
	}
	// Session headers of the applying affinity rule; keep_origin keeps the
	// channel's and the plugin's explicit headers.
	st.affinity.PassHeaders().Apply(req.Header, r.Header, rt.Config.Headers, spec.Headers)
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", g.opts.UserAgent)
	}

	errHeaderTimeout := errors.New("upstream header timeout")
	timer := time.AfterFunc(rt.Timeout(), func() { cancel(errHeaderTimeout) })
	resp, err := g.reg.Do(rt, req)
	timer.Stop()
	if err != nil {
		switch {
		case r.Context().Err() != nil:
			return fail(protocol.NewError(protocol.ErrClientClosed, "client closed request"), "", false)
		case errors.Is(context.Cause(ctx), errHeaderTimeout):
			return fail(protocol.NewError(protocol.ErrUpstreamTimeout, fmt.Sprintf("upstream did not respond within %s", rt.Timeout())), routing.RetryTimeout, true)
		case errors.Is(err, netguard.ErrBlocked):
			return fail(protocol.NewError(protocol.ErrUpstreamUnavailable, "upstream address blocked by network policy"), routing.RetryNetwork, true)
		default:
			return fail(protocol.NewError(protocol.ErrUpstreamUnavailable, "upstream connection failed"), routing.RetryNetwork, true)
		}
	}
	defer resp.Body.Close()
	att.StatusCode = resp.StatusCode
	ttft := time.Since(st.start).Milliseconds()
	firstByte := time.Since(attStart)

	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		status, msg := resp.StatusCode, protocol.UpstreamErrorMessage(raw)
		if ps.HasNormalizeError() {
			ne, _, err := ps.NormalizeError(ctx, plugin.NewUpstreamResponse(resp.StatusCode, resp.Header, raw))
			if err != nil {
				// Fall back to the status code classification.
				g.log.WarnContext(r.Context(), "normalizeError failed", "channel_id", rt.ID, "plugin", rt.Plugin.Key, "err", err)
			} else {
				status, msg = ne.Status, protocol.Redact(truncateMsg(ne.Message))
				if status < 400 {
					status = http.StatusBadGateway
				}
			}
		}
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			g.reg.ReportAuthFailure(rt.ID, status)
		}
		e, class, health := classifyStatus(status, "upstream: "+msg)
		return fail(e, class, health)
	}

	var gerr *protocol.GatewayError
	var class string
	if st.info.Stream {
		gerr, class = g.customStream(ctx, w, r, st, rt, ps, resp, ttft, fail)
	} else {
		gerr, class = g.customUnary(ctx, w, r, st, rt, ps, resp, ttft, fail)
	}
	if gerr == nil {
		g.latency.Observe(rt.ID, st.info.Model, firstByte)
	}
	return gerr, class
}

func (g *Gateway) customUnary(ctx context.Context, w http.ResponseWriter, r *http.Request, st *reqState, rt *channel.Runtime,
	ps *plugin.ProtocolSession, resp *http.Response, ttft int64, fail failFunc) (*protocol.GatewayError, string) {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, g.opts.MaxRespBytes+1))
	if err != nil {
		return fail(protocol.NewError(protocol.ErrUpstreamUnavailable, "failed to read upstream response"), routing.RetryNetwork, true)
	}
	if int64(len(raw)) > g.opts.MaxRespBytes {
		return fail(protocol.NewError(protocol.ErrUpstreamInvalid, "upstream response too large"), "", false)
	}
	out, err := ps.ParseResponse(ctx, plugin.NewUpstreamResponse(resp.StatusCode, resp.Header, raw))
	if err != nil {
		g.log.WarnContext(r.Context(), "parseResponse failed", "channel_id", rt.ID, "plugin", rt.Plugin.Key, "err", err)
		return fail(pluginError(err), routing.RetryServerError, true)
	}
	chat, usage, hasUsage, err := protocol.NormalizeCanonicalResponse(out, st.info.Model)
	var ce *protocol.CanonicalError
	if errors.As(err, &ce) {
		return fail(canonicalFailure(ce))
	}
	if err != nil {
		return fail(pluginError(err), routing.RetryServerError, true)
	}
	final := chat
	if st.dialect != protocol.OpenAIChat {
		if final, _, err = protocol.ConvertResponse(st.dialect, protocol.OpenAIChat, chat, st.info.Model); err != nil {
			return fail(pluginError(err), routing.RetryServerError, true)
		}
	}
	if !hasUsage {
		usage = protocol.Usage{Input: protocol.EstimateTokens(len(st.body)), Output: protocol.EstimateTokens(len(raw)), Estimated: true}
	}
	g.reg.Breaker.Success(rt.ID)
	g.writeHeaders(w, st, "application/json")
	_, _ = w.Write(final)
	st.entry.StatusCode = http.StatusOK
	st.entry.TTFTMs = &ttft
	st.entry.Usage = usage
	return nil, ""
}

func (g *Gateway) customStream(ctx context.Context, w http.ResponseWriter, r *http.Request, st *reqState, rt *channel.Runtime,
	ps *plugin.ProtocolSession, resp *http.Response, ttft int64, fail failFunc) (*protocol.GatewayError, string) {
	cs, err := protocol.NewCanonicalStream(st.dialect, st.info.Model, st.info.IncludeUsage)
	if err != nil {
		return fail(protocol.NewError(protocol.ErrInternal, err.Error()), "", false)
	}
	flusher, _ := w.(http.Flusher)
	idle := time.AfterFunc(g.opts.StreamIdle, func() { resp.Body.Close() })
	defer idle.Stop()

	var firstByte *int64
	emit := func(b []byte) bool {
		if len(b) == 0 {
			return true
		}
		if !st.written {
			g.writeHeaders(w, st, "text/event-stream; charset=utf-8")
			t := time.Since(st.start).Milliseconds()
			firstByte = &t
		}
		if _, err := w.Write(b); err != nil {
			return false
		}
		if flusher != nil {
			flusher.Flush()
		}
		return true
	}

	var streamErr *protocol.GatewayError
	streamClass, streamHealth := routing.RetryServerError, true
	clientGone := func() {
		streamErr, streamClass, streamHealth = protocol.NewError(protocol.ErrClientClosed, "client closed stream"), "", false
	}
	// events converts one hook result; false stops the stream (streamErr set).
	events := func(raw json.RawMessage, hookErr error) bool {
		if hookErr != nil {
			g.log.WarnContext(r.Context(), "custom protocol stream hook failed", "channel_id", rt.ID, "plugin", rt.Plugin.Key, "err", hookErr)
			streamErr = pluginError(hookErr)
			return false
		}
		if raw == nil {
			return true
		}
		var evs []protocol.CanonicalEvent
		if err := json.Unmarshal(raw, &evs); err != nil {
			streamErr = pluginError(fmt.Errorf("事件格式错误：%v", err))
			return false
		}
		out, err := cs.Events(evs)
		ok := emit(out)
		var ce *protocol.CanonicalError
		switch {
		case errors.As(err, &ce):
			streamErr, streamClass, streamHealth = canonicalFailure(ce)
			return false
		case err != nil:
			streamErr = pluginError(err)
			return false
		case !ok:
			clientGone()
			return false
		}
		return true
	}

	buf := make([]byte, 32<<10)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			idle.Reset(g.opts.StreamIdle)
			if !events(ps.ParseStream(ctx, buf[:n])) {
				break
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			if r.Context().Err() != nil {
				clientGone()
			} else {
				streamErr = protocol.NewError(protocol.ErrUpstreamInvalid, "upstream stream interrupted")
				streamClass = routing.RetryNetwork
			}
			break
		}
	}
	if streamErr == nil && events(ps.EndStream(ctx)) {
		tail, err := cs.Finish()
		ok := emit(tail)
		switch {
		case err != nil:
			streamErr = pluginError(err)
		case !ok:
			clientGone()
		}
	}

	usage, ok := cs.Usage()
	if !ok || (usage.Output == 0 && cs.OutputBytes() > 0) {
		if !ok {
			usage.Input = protocol.EstimateTokens(len(st.body))
		}
		usage.Output = protocol.EstimateTokens(cs.OutputBytes())
		usage.Estimated = true
	}

	if streamErr != nil && !st.written {
		// Nothing reached the client: safe to try another channel.
		return fail(streamErr, streamClass, streamHealth)
	}
	if streamErr != nil && streamErr.Class != protocol.ErrClientClosed {
		emit(protocol.EncodeStreamError(st.dialect, streamErr))
		g.reg.Breaker.Failure(rt.ID, streamErr.Message)
	} else {
		g.reg.Breaker.Success(rt.ID)
	}
	st.entry.StatusCode = http.StatusOK
	if streamErr != nil {
		c, m := streamErr.Class, streamErr.Message
		st.entry.ErrorClass, st.entry.ErrorMessage = &c, &m
		if c == protocol.ErrClientClosed {
			st.entry.StatusCode = 499
		}
	}
	if firstByte != nil {
		st.entry.TTFTMs = firstByte
	} else {
		st.entry.TTFTMs = &ttft
	}
	st.entry.Usage = usage
	return nil, ""
}

// billingCtx is the BillingCtx custom meters receive (phase9-api.md §3).
func (st *reqState) billingCtx(served string) subscription.BillingCtx {
	e := st.entry
	bc := subscription.BillingCtx{Model: e.Model, ServedModel: served, ChannelTier: e.ChannelTier, Inbound: st.dialect,
		ImageCount: e.Usage.Images, AudioSeconds: float64(e.Usage.AudioSeconds)}
	if e.ChannelID != nil {
		bc.ChannelID = e.ChannelID.String()
	}
	if st.group != nil {
		bc.UserGroup = st.group.Name
	}
	return bc
}
