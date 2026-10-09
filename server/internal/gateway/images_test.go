package gateway

import (
	"bytes"
	"crypto/rand"
	"io"
	"mime/multipart"
	"os"
	"strings"
	"testing"
)

func TestSpoolSpillsToDiskAndCleansUp(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	s := &spool{limit: 1 << 10}
	data := make([]byte, 5000)
	_, _ = rand.Read(data)
	for i := 0; i < len(data); i += 700 {
		if _, err := s.Write(data[i:min(i+700, len(data))]); err != nil {
			t.Fatal(err)
		}
	}
	if s.file == nil {
		t.Fatal("spool stayed in memory above its limit")
	}
	name := s.file.Name()
	for i := 0; i < 2; i++ { // replayable
		got, _ := io.ReadAll(s.reader())
		if !bytes.Equal(got, data) {
			t.Fatal("spooled bytes differ")
		}
	}
	s.close()
	if _, err := os.Stat(name); !os.IsNotExist(err) {
		t.Fatalf("temporary file not removed: %v", err)
	}
	small := &spool{limit: 1 << 10}
	_, _ = small.Write([]byte("abc"))
	if small.file != nil {
		t.Fatal("small body spilled to disk")
	}
}

func TestMultipartReencode(t *testing.T) {
	var src bytes.Buffer
	mw := multipart.NewWriter(&src)
	_ = mw.WriteField("prompt", "p")
	fw, _ := mw.CreateFormFile("image[]", "a.png")
	img := bytes.Repeat([]byte{0, 1, 2, 3, '\r', '\n', '-', '-'}, 4096)
	_, _ = fw.Write(img)
	_ = mw.WriteField("model", "client-model")
	_ = mw.Close()

	m := &multipartBody{spool: &spool{limit: 1 << 20}, boundary: mw.Boundary(), out: "omnigate-test-boundary"}
	_, _ = m.spool.Write(src.Bytes())
	n, err := m.length("up-model")
	if err != nil {
		t.Fatal(err)
	}
	body, length, stop, err := m.stream("up-model")
	if err != nil {
		t.Fatal(err)
	}
	enc, err := io.ReadAll(body)
	stop()
	if err != nil || int64(len(enc)) != n || length != n {
		t.Fatalf("encoded %d bytes, length %d/%d, err %v", len(enc), n, length, err)
	}
	if !strings.Contains(m.contentType(), "boundary=omnigate-test-boundary") {
		t.Fatalf("content type = %s", m.contentType())
	}
	r := multipart.NewReader(bytes.NewReader(enc), "omnigate-test-boundary")
	form, err := r.ReadForm(1 << 20)
	if err != nil {
		t.Fatal(err)
	}
	if form.Value["model"][0] != "up-model" || form.Value["prompt"][0] != "p" {
		t.Fatalf("fields = %v", form.Value)
	}
	f, _ := form.File["image[]"][0].Open()
	got, _ := io.ReadAll(f)
	if !bytes.Equal(got, img) || form.File["image[]"][0].Filename != "a.png" {
		t.Fatal("file part changed")
	}
	// Stopping an encoder the transport never read does not leak it.
	_, _, stop, _ = m.stream("other")
	stop()
}
