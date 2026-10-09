package protocol

import (
	"strings"
	"testing"
)

func TestImageUsageAndInfo(t *testing.T) {
	u, ok, err := UsageFromImagesResponse([]byte(`{"created":1,"data":[{"b64_json":"a"},{"url":"https://x"}],
		"usage":{"input_tokens":50,"output_tokens":1000,"total_tokens":1050,"input_tokens_details":{"text_tokens":20,"image_tokens":30}}}`))
	if err != nil || !ok || u.Images != 2 || u.Input != 50 || u.ImageInput != 30 || u.Output != 1000 {
		t.Fatalf("usage = %+v %v %v", u, ok, err)
	}
	u, ok, err = UsageFromImagesResponse([]byte(`{"created":1,"data":[{"url":"https://x"}]}`))
	if err != nil || ok || u.Images != 1 || u.Input != 0 || u.Estimated {
		t.Fatalf("dall-e usage = %+v %v %v", u, ok, err)
	}
	if _, _, err := UsageFromImagesResponse([]byte(`not json`)); err == nil {
		t.Fatal("malformed response accepted")
	}
	info, err := ParseImageInfo([]byte(`{"model":"gpt-image-1","prompt":"a cat","n":3,"stream":true}`))
	if err != nil || info.Model != "gpt-image-1" || info.Images != 3 || !info.Stream || info.PromptBytes != len(`"a cat"`) {
		t.Fatalf("info = %+v %v", info, err)
	}
	if info, _ := ParseImageInfo([]byte(`{"model":"m","n":0}`)); info.Images != 1 {
		t.Fatalf("n default = %d", info.Images)
	}
	if info, _ := ParseImageInfo([]byte(`{"model":"m","n":100000}`)); info.Images != MaxImageCount {
		t.Fatalf("n clamp = %d", info.Images)
	}
	if _, err := ParseImageInfo([]byte(`{"prompt":"x"}`)); err == nil {
		t.Fatal("missing model accepted")
	}
}

func TestImageStreamProcessor(t *testing.T) {
	p := NewImageStreamProcessor()
	stream := "event: image_edit.partial_image\ndata: {\"type\":\"image_edit.partial_image\",\"b64_json\":\"cA==\"}\n\n" +
		"event: image_edit.completed\ndata: {\"type\":\"image_edit.completed\",\"b64_json\":\"aQ==\",\"usage\":{\"input_tokens\":40,\"output_tokens\":200,\"input_tokens_details\":{\"image_tokens\":25}}}\n\n" +
		"event: image_edit.completed\ndata: {\"type\":\"image_edit.completed\",\"b64_json\":\"aQ==\",\"usage\":{\"input_tokens\":40,\"output_tokens\":200,\"input_tokens_details\":{\"image_tokens\":25}}}\n\n"
	r := NewSSEReader(strings.NewReader(stream))
	var out strings.Builder
	for {
		ev, err := r.Next()
		if err != nil {
			break
		}
		if p.Done() && out.Len() == 0 {
			t.Fatal("done before completed")
		}
		b, err := p.Process(ev)
		if err != nil {
			t.Fatal(err)
		}
		out.Write(b)
	}
	if out.String() != stream {
		t.Fatalf("passthrough changed the stream:\n%q", out.String())
	}
	u, ok := p.Usage()
	if !p.Done() || !ok || u.Images != 2 || u.Input != 40 || u.Output != 200 || u.ImageInput != 25 {
		t.Fatalf("usage = %+v ok=%v done=%v", u, ok, p.Done())
	}
	q := NewImageStreamProcessor()
	_, _ = q.Process(&Event{Data: []byte(`{"type":"image_generation.partial_image"}`)})
	if q.Done() {
		t.Fatal("partial image is not terminal")
	}
	if u, ok := q.Usage(); ok || u.Images != 0 {
		t.Fatalf("partial usage = %+v", u)
	}
}
