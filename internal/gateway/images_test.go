package gateway

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"bpslocal/internal/identity"
)

// Only the network boundary is substituted; multipart generation, validation,
// caching and Responses conversion are exercised through the real gateway.
type attachmentTestTransport struct {
	destination *url.URL
	base        http.RoundTripper
}

func (tr attachmentTestTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Host == "bps.openai.com" {
		clone := r.Clone(r.Context())
		u := *r.URL
		u.Scheme = tr.destination.Scheme
		u.Host = tr.destination.Host
		clone.URL = &u
		return tr.base.RoundTrip(clone)
	}
	return tr.base.RoundTrip(r)
}
func imageGateway(t *testing.T, h http.HandlerFunc) *Gateway {
	t.Helper()
	g := gateway(t, h)
	destination, _ := url.Parse(g.endpoint)
	g.client.Transport = attachmentTestTransport{destination: destination, base: g.client.Transport}
	return g
}
func inlinePNG(t *testing.T) (string, []byte) {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 2, 3))); err != nil {
		t.Fatal(err)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(b.Bytes()), b.Bytes()
}
func imageRequest(t *testing.T, parts ...map[string]interface{}) string {
	t.Helper()
	raw, err := json.Marshal(map[string]interface{}{"model": "gpt-6-astra", "stream": true, "input": []interface{}{map[string]interface{}{"role": "user", "content": parts}}})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
func imagePart(data string) map[string]interface{} {
	return map[string]interface{}{"type": "input_image", "image_url": data, "detail": "original"}
}

func TestNativeImageUploadsBeforeResponses(t *testing.T) {
	data, pngBytes := inlinePNG(t)
	var uploads, responses int
	g := imageGateway(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/basispoints/api/attachments" {
			uploads++
			if r.Header.Get("Authorization") != "Bearer "+token() || r.Header.Get("Chatgpt-Account-Id") != "account-1" {
				t.Error("wrong upload identity")
			}
			if r.Header.Get("Accept") != "application/json" {
				t.Error("wrong upload accept")
			}
			mr, err := r.MultipartReader()
			if err != nil {
				t.Error(err)
				return
			}
			part, err := mr.NextPart()
			if err != nil {
				t.Error(err)
				return
			}
			body, _ := io.ReadAll(part)
			if part.FormName() != "file" || part.FileName() != "image.png" || !bytes.Equal(body, pngBytes) {
				t.Error("incorrect multipart image")
			}
			fmt.Fprint(w, `{"openai_file_id":"file-native1"}`)
			return
		}
		responses++
		body, _ := io.ReadAll(r.Body)
		if uploads != 1 || !bytes.Contains(body, []byte(`"file_id":"file-native1"`)) || bytes.Contains(body, []byte("data:image")) || bytes.Contains(body, []byte("file-preflight")) {
			t.Error("image was not replaced after upload", string(body))
		}
		if !bytes.Contains(body, []byte("describe this")) || !bytes.Contains(body, []byte("https://example.com/p.png")) {
			t.Error("neighboring content changed")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, completed)
	})
	body := imageRequest(t, map[string]interface{}{"type": "input_text", "text": "describe this"}, imagePart(data), imagePart(data), imagePart("https://example.com/p.png"))
	for _, compact := range []bool{false, true} {
		r := request(body)
		if compact {
			r.URL.Path = "/v1/responses/compact"
		}
		w := httptest.NewRecorder()
		g.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("native images rejected: %d %s", w.Code, w.Body.String())
		}
	}
	if uploads != 1 || responses != 2 {
		t.Fatalf("expected scoped image reuse: uploads=%d responses=%d", uploads, responses)
	}
}

func TestNativeImagesValidateEntireRequestBeforeUpload(t *testing.T) {
	data, _ := inlinePNG(t)
	good := imageRequest(t, imagePart(data))
	for name, body := range map[string]string{
		"bad second image": imageRequest(t, imagePart(data), imagePart("data:image/png;base64,PRIVATE_BAD")),
		"mime mismatch":    imageRequest(t, imagePart(strings.Replace(data, "image/png", "image/jpeg", 1))),
		"invalid schema":   strings.Replace(good, `"stream":true`, `"stream":true,"text":{"format":{"type":"json_schema"}}`, 1),
		"wrong model":      strings.Replace(good, "gpt-6-astra", "another-model", 1),
		"too many": imageRequest(t, func() []map[string]interface{} {
			p := make([]map[string]interface{}, 21)
			for i := range p {
				p[i] = imagePart(data)
			}
			return p
		}()...),
	} {
		t.Run(name, func(t *testing.T) {
			var called atomic.Int32
			g := imageGateway(t, func(http.ResponseWriter, *http.Request) { called.Add(1) })
			w := httptest.NewRecorder()
			g.ServeHTTP(w, request(body))
			if w.Code < 400 || called.Load() != 0 || strings.Contains(w.Body.String(), "PRIVATE_BAD") {
				t.Fatal(w.Code, called.Load(), w.Body.String())
			}
		})
	}
}

func TestNativeUploadFailureNeverStartsResponsesOrLeaksBody(t *testing.T) {
	data, _ := inlinePNG(t)
	for _, status := range []int{401, 403, 429, 500, 302} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var uploads, responses atomic.Int32
			g := imageGateway(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/basispoints/api/attachments" {
					responses.Add(1)
					return
				}
				uploads.Add(1)
				w.Header().Set("Location", "https://elsewhere.invalid/stolen")
				w.WriteHeader(status)
				fmt.Fprint(w, "PRIVATE_IMAGE "+token())
			})
			w := httptest.NewRecorder()
			g.ServeHTTP(w, request(imageRequest(t, imagePart(data))))
			want := status
			if status == 302 {
				want = 502
			}
			if w.Code != want || uploads.Load() != 1 || responses.Load() != 0 || strings.Contains(w.Body.String(), "PRIVATE_IMAGE") || strings.Contains(w.Body.String(), token()) {
				t.Fatal(w.Code, uploads.Load(), responses.Load(), w.Body.String())
			}
		})
	}
}

func TestNativeAttachmentCacheIsolatesAccountCredentialAndThread(t *testing.T) {
	data, _ := inlinePNG(t)
	var uploads atomic.Int32
	account, _ := testAccount()
	g := imageGateway(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/basispoints/api/attachments" {
			n := uploads.Add(1)
			fmt.Fprintf(w, `{"openai_file_id":"file-upload%d"}`, n)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, completed)
	})
	g.accountSource = func() (identity.Account, error) { return account, nil }
	cases := []struct {
		account, credential, thread string
		want                        int32
	}{
		{"account-1", token(), "t1", 1}, {"account-1", token(), "t1", 1}, {"account-2", token(), "t1", 2},
		{"account-2", "new-fake-token", "t1", 3}, {"account-2", "new-fake-token", "t2", 4},
		{"account-2", "new-fake-token", "", 5}, {"account-2", "new-fake-token", "", 6},
	}
	for _, c := range cases {
		account.AccountID = c.account
		account.AccessToken = c.credential
		r := request(imageRequest(t, imagePart(data)))
		r.Header.Set("thread-id", c.thread)
		w := httptest.NewRecorder()
		g.ServeHTTP(w, r)
		if w.Code != 200 || uploads.Load() != c.want {
			t.Fatal(w.Code, uploads.Load(), c.want, w.Body.String())
		}
	}
}

func TestNativeUploadCancellationStopsBeforeResponses(t *testing.T) {
	data, _ := inlinePNG(t)
	started := make(chan struct{})
	cancelled := make(chan struct{})
	var responses atomic.Int32
	g := imageGateway(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/basispoints/api/attachments" {
			responses.Add(1)
			return
		}
		io.Copy(io.Discard, r.Body)
		close(started)
		<-r.Context().Done()
		close(cancelled)
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	w := httptest.NewRecorder()
	go func() { defer close(done); g.ServeHTTP(w, request(imageRequest(t, imagePart(data))).WithContext(ctx)) }()
	select {
	case <-started:
	case <-done:
		t.Fatalf("upload never started: %s", w.Body.String())
	case <-time.After(3 * time.Second):
		t.Fatal("upload did not start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("request ignored cancellation")
	}
	select {
	case <-cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("upload ignored cancellation")
	}
	if responses.Load() != 0 {
		t.Fatal("responses started after cancelled upload")
	}
}

func TestNativeToolScreenshotsAndOrdinaryNonStream(t *testing.T) {
	data, _ := inlinePNG(t)
	for _, kind := range []string{"function_call", "custom_tool_call"} {
		t.Run(kind, func(t *testing.T) {
			var uploads atomic.Int32
			g := imageGateway(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/basispoints/api/attachments" {
					uploads.Add(1)
					fmt.Fprint(w, `{"openai_file_id":"file-toolshot"}`)
					return
				}
				body, _ := io.ReadAll(r.Body)
				// v2.9.3 工具截图保留内联，不再上传附件。
				if bytes.Contains(body, []byte(`"file_id":"file-toolshot"`)) || !bytes.Contains(body, []byte(`"type":"function_call_output"`)) || !bytes.Contains(body, []byte(data)) {
					t.Error("tool screenshot lost", string(body))
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, completed)
			})
			call := map[string]interface{}{"type": kind, "name": "view_image", "call_id": "call_screenshot"}
			outputType := "function_call_output"
			if kind == "function_call" {
				call["arguments"] = `{}`
			} else {
				call["input"] = "screen.png"
				outputType = "custom_tool_call_output"
			}
			body, err := json.Marshal(map[string]interface{}{"model": "gpt-6-astra", "stream": false, "input": []interface{}{
				map[string]interface{}{"role": "user", "content": "inspect"}, call,
				map[string]interface{}{"type": outputType, "call_id": "call_screenshot", "output": []interface{}{imagePart(data)}},
			}})
			if err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			g.ServeHTTP(w, request(string(body)))
			if w.Code != 200 || uploads.Load() != 0 || strings.Contains(w.Header().Get("Content-Type"), "event-stream") || !strings.Contains(w.Body.String(), `"status":"completed"`) {
				t.Fatal(w.Code, uploads.Load(), w.Body.String())
			}
		})
	}
}

func TestNativeMalformedUploadResponsesFailClosed(t *testing.T) {
	data, _ := inlinePNG(t)
	for _, body := range []string{`{"id":"file-wrong-field"}`, `{"openai_file_id":"https://host/private"}`, `{"openai_file_id":"file-good"} {}`, strings.Repeat("x", (64<<10)+1), `{"openai_file_id":"file-private?token=secret"}`} {
		t.Run(fmt.Sprint(len(body)), func(t *testing.T) {
			var uploads, responses atomic.Int32
			g := imageGateway(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/basispoints/api/attachments" {
					uploads.Add(1)
					fmt.Fprint(w, body)
					return
				}
				responses.Add(1)
			})
			w := httptest.NewRecorder()
			g.ServeHTTP(w, request(imageRequest(t, imagePart(data))))
			if w.Code != 502 || uploads.Load() != 1 || responses.Load() != 0 || strings.Contains(w.Body.String(), "secret") {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
}

func TestImageRequestBudgetRejectsBeforeReadingAndReleases(t *testing.T) {
	var called atomic.Int32
	g := imageGateway(t, func(w http.ResponseWriter, r *http.Request) {
		called.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, completed)
	})
	g.bodyBytes.Store(bodyMemoryBudget)
	w := httptest.NewRecorder()
	g.ServeHTTP(w, request(simpleRequest))
	if w.Code != 503 || called.Load() != 0 || g.bodyBytes.Load() != bodyMemoryBudget {
		t.Fatal(w.Code, called.Load(), g.bodyBytes.Load())
	}
	g.bodyBytes.Store(0)
	r := request(simpleRequest)
	r.ContentLength = MaxBody + 1
	w = httptest.NewRecorder()
	g.ServeHTTP(w, r)
	if w.Code != 413 || called.Load() != 0 {
		t.Fatal(w.Code, called.Load())
	}
	for _, body := range []string{simpleRequest, `{broken}`} {
		w = httptest.NewRecorder()
		g.ServeHTTP(w, request(body))
		if g.bodyBytes.Load() != 0 {
			t.Fatal("request reservation leaked")
		}
	}
}

func TestNativeUploadTimeoutNeverStartsResponses(t *testing.T) {
	data, _ := inlinePNG(t)
	var responses atomic.Int32
	g := imageGateway(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/basispoints/api/attachments" {
			io.Copy(io.Discard, r.Body)
			<-r.Context().Done()
			return
		}
		responses.Add(1)
	})
	g.requestTimeout = 35 * time.Millisecond
	var result Result
	g.report = func(r Result) { result = r }
	w := httptest.NewRecorder()
	g.ServeHTTP(w, request(imageRequest(t, imagePart(data))))
	if w.Code != 504 || responses.Load() != 0 || result.Code != codeUploadTimeout || result.Cancelled {
		t.Fatal(w.Code, result)
	}
}
