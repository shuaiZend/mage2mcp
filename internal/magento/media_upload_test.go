// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

// TestUploadMedia encodes Magento's real contract: POST /products/{sku}/media
// returns the new gallery entry id as a JSON integer
// (ProductAttributeMediaGalleryManagementInterface::create(): int), and the
// gateway resolves the full entry with a gallery read.
func TestUploadMedia(t *testing.T) {
	var create, read recordedWrite
	var body mediaUploadBody
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := recordedWrite{Method: r.Method, Path: r.URL.Path}
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodPost:
			raw, _ := io.ReadAll(r.Body)
			rec.Body = raw
			create = rec
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Errorf("decode body: %v", err)
			}
			// Magento returns a bare integer, not a string and not an object.
			_, _ = w.Write([]byte(`9`))
		case http.MethodGet:
			read = rec
			_, _ = w.Write([]byte(`[{"id":9,"media_type":"image","label":"Front","position":1,` +
				`"types":["image","small_image"],"file":"/w/i/front.jpg"}]`))
		}
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	content := base64.StdEncoding.EncodeToString([]byte("fake-image-bytes"))
	entry, err := svc.UploadMedia(context.Background(), "SKU-1", MediaUploadInput{
		Label: "Front", Position: 1, Types: []string{"image", "small_image"},
		Filename: "front.jpg", MIMEType: "image/jpeg", DataBase64: content,
	})
	if err != nil {
		t.Fatalf("integer entry id must be accepted: %v", err)
	}
	if create.Method != http.MethodPost || create.Path != "/rest/default/V1/products/SKU-1/media" {
		t.Fatalf("request = %s %s", create.Method, create.Path)
	}
	if body.Entry.MediaType != "image" || body.Entry.Content.Type != "image/jpeg" || body.Entry.Content.Name != "front.jpg" {
		t.Fatalf("body = %+v", body)
	}
	if body.Entry.Content.Base64 != content || len(body.Entry.Types) != 2 {
		t.Fatalf("body = %+v", body)
	}
	if entry.ID != 9 || entry.Label != "Front" || entry.File != "/w/i/front.jpg" {
		t.Fatalf("entry = %+v", entry)
	}
	if read.Method != http.MethodGet || read.Path != "/rest/default/V1/products/SKU-1/media" {
		t.Fatalf("the entry must be resolved by reading the gallery: %+v", read)
	}
}

// TestUploadMediaObjectResponse keeps the full-entry response shape working.
func TestUploadMediaObjectResponse(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":9,"media_type":"image","label":"Front","position":1,"file":"/w/i/front.jpg"}`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	entry, err := svc.UploadMedia(context.Background(), "SKU-1", MediaUploadInput{
		Filename: "front.jpg", MIMEType: "image/jpeg", DataBase64: base64.StdEncoding.EncodeToString([]byte("x")),
	})
	if err != nil {
		t.Fatalf("object response must be accepted: %v", err)
	}
	if entry.ID != 9 || entry.File != "/w/i/front.jpg" {
		t.Fatalf("entry = %+v", entry)
	}
}

// TestUploadMediaRejectsInvalidEntryID guards the scalar decoder: an id that
// cannot address a gallery entry is an upstream defect, not a success.
func TestUploadMediaRejectsInvalidEntryID(t *testing.T) {
	for _, body := range []string{`0`, `-5`, `"not-an-id"`} {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, body)
		}))
		svc := testService(t, ts.URL)
		_, err := svc.UploadMedia(context.Background(), "SKU-1", MediaUploadInput{
			Filename: "front.jpg", MIMEType: "image/jpeg", DataBase64: base64.StdEncoding.EncodeToString([]byte("x")),
		})
		ts.Close()
		if errs.CodeOf(err) != errs.CodeUpstreamUnavailable {
			t.Fatalf("body %q: err = %v, want upstream_unavailable", body, err)
		}
	}
}

func TestUploadMediaValidation(t *testing.T) {
	svc := testService(t, "https://shop.example.com")
	ctx := context.Background()
	valid := MediaUploadInput{
		Filename: "front.jpg", MIMEType: "image/jpeg",
		DataBase64: base64.StdEncoding.EncodeToString([]byte("x")),
	}
	cases := []struct {
		name string
		sku  string
		in   MediaUploadInput
	}{
		{"bad sku", "bad sku!", valid},
		{"bad mime", "SKU-1", MediaUploadInput{Filename: "a.jpg", MIMEType: "application/pdf", DataBase64: valid.DataBase64}},
		{"bad filename", "SKU-1", MediaUploadInput{Filename: "../a.jpg", MIMEType: "image/jpeg", DataBase64: valid.DataBase64}},
		{"bad label", "SKU-1", MediaUploadInput{Filename: "a.jpg", MIMEType: "image/jpeg", DataBase64: valid.DataBase64, Label: strings.Repeat("x", 256)}},
		{"bad position", "SKU-1", MediaUploadInput{Filename: "a.jpg", MIMEType: "image/jpeg", DataBase64: valid.DataBase64, Position: 1001}},
		{"bad type", "SKU-1", MediaUploadInput{Filename: "a.jpg", MIMEType: "image/jpeg", DataBase64: valid.DataBase64, Types: []string{"video"}}},
		{"duplicate type", "SKU-1", MediaUploadInput{Filename: "a.jpg", MIMEType: "image/jpeg", DataBase64: valid.DataBase64, Types: []string{"image", "image"}}},
		{"bad base64", "SKU-1", MediaUploadInput{Filename: "a.jpg", MIMEType: "image/jpeg", DataBase64: "!!!not-base64!!!"}},
		{"empty content", "SKU-1", MediaUploadInput{Filename: "a.jpg", MIMEType: "image/jpeg"}},
	}
	for _, tc := range cases {
		if _, err := svc.UploadMedia(ctx, tc.sku, tc.in); errs.CodeOf(err) != errs.CodeInvalidArgument {
			t.Fatalf("%s: err = %v", tc.name, err)
		}
	}
}

func TestUploadMediaRejectsOversizedPayload(t *testing.T) {
	svc := testService(t, "https://shop.example.com")
	// 5 MiB + 1 byte decoded; the check must reject before decoding.
	oversized := base64.StdEncoding.EncodeToString(make([]byte, maxMediaBytes+1))
	_, err := svc.UploadMedia(context.Background(), "SKU-1", MediaUploadInput{
		Filename: "big.png", MIMEType: "image/png", DataBase64: oversized,
	})
	if errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("err = %v", err)
	}
}

func TestUpdateMedia(t *testing.T) {
	var method, path string
	var body mediaUpdateBody
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":9,"label":"New label","position":2,"types":["image"],"disabled":true}`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	label := "New label"
	position := 2
	disabled := true
	entry, err := svc.UpdateMedia(context.Background(), "SKU-1", 9, MediaUpdateInput{
		Label: &label, Position: &position, Types: []string{"image"}, Disabled: &disabled,
	})
	if err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPut || path != "/rest/default/V1/products/SKU-1/media/9" {
		t.Fatalf("request = %s %s", method, path)
	}
	if body.Entry.ID != 9 || body.Entry.Label == nil || *body.Entry.Label != "New label" {
		t.Fatalf("body = %+v", body.Entry)
	}
	if entry.ID != 9 || entry.Label != "New label" || !entry.Disabled {
		t.Fatalf("entry = %+v", entry)
	}
}

func TestUpdateMediaValidation(t *testing.T) {
	svc := testService(t, "https://shop.example.com")
	ctx := context.Background()
	if _, err := svc.UpdateMedia(ctx, "SKU-1", 0, MediaUpdateInput{Types: []string{"image"}}); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("id err = %v", err)
	}
	if _, err := svc.UpdateMedia(ctx, "SKU-1", 9, MediaUpdateInput{}); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("empty err = %v", err)
	}
	label := strings.Repeat("x", 256)
	if _, err := svc.UpdateMedia(ctx, "SKU-1", 9, MediaUpdateInput{Label: &label}); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("label err = %v", err)
	}
	position := 1001
	if _, err := svc.UpdateMedia(ctx, "SKU-1", 9, MediaUpdateInput{Position: &position}); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("position err = %v", err)
	}
	if _, err := svc.UpdateMedia(ctx, "SKU-1", 9, MediaUpdateInput{Types: []string{"video"}}); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("type err = %v", err)
	}
}

func TestGetMedia(t *testing.T) {
	var path string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":9,"file":"/e/9.jpg","label":"Main","position":1,"types":["image"],"disabled":false}`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	entry, err := svc.GetMedia(context.Background(), "SKU-1", 9)
	if err != nil {
		t.Fatal(err)
	}
	if path != "/rest/default/V1/products/SKU-1/media/9" {
		t.Fatalf("path = %s", path)
	}
	if entry.ID != 9 || entry.Label != "Main" || len(entry.Types) != 1 {
		t.Fatalf("entry = %+v", entry)
	}
	if _, err := svc.GetMedia(context.Background(), "SKU-1", 0); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("validation err = %v", err)
	}
}

func TestMediaTypes(t *testing.T) {
	var path string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"code":"image","label":"Base Image"},{"code":"thumbnail","label":"Thumbnail"}]`))
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	types, err := svc.MediaTypes(context.Background(), "Default")
	if err != nil {
		t.Fatal(err)
	}
	if path != "/rest/default/V1/products/media/types/Default" {
		t.Fatalf("path = %s", path)
	}
	if len(types) != 2 || types[0].Code != "image" || types[1].Label != "Thumbnail" {
		t.Fatalf("types = %+v", types)
	}
	if _, err := svc.MediaTypes(context.Background(), " "); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("validation err = %v", err)
	}
}

func TestUploadMediaScalarResponse(t *testing.T) {
	var paths []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/rest/default/V1/products/SKU-1/media":
			_, _ = w.Write([]byte(`"3422"`))
		case r.Method == http.MethodGet && r.URL.Path == "/rest/default/V1/products/SKU-1/media":
			_, _ = w.Write([]byte(`[{"id":3422,"media_type":"image","label":"Demo","position":1,"types":["image"],"file":"/m/d/demo.png"}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	entry, err := svc.UploadMedia(context.Background(), "SKU-1", MediaUploadInput{
		Filename: "demo.png", MIMEType: "image/png", DataBase64: "aGk=", Label: "Demo", Position: 1, Types: []string{"image"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if entry.ID != 3422 || entry.File != "/m/d/demo.png" || entry.Label != "Demo" {
		t.Fatalf("entry = %+v", entry)
	}
	if len(paths) != 2 {
		t.Fatalf("paths = %v", paths)
	}
}

func TestUpdateMediaSendsCompleteEntry(t *testing.T) {
	var body mediaUpdateBody
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			_, _ = w.Write([]byte(`{"id":9,"media_type":"image","file":"/m/d/demo.png","label":"Old","position":1,"types":["image"]}`))
		case http.MethodPut:
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &body)
			_, _ = w.Write([]byte(`{"id":9,"label":"New","position":2,"types":["image","thumbnail"],"file":"/m/d/demo.png"}`))
		}
	}))
	defer ts.Close()
	svc := testService(t, ts.URL)
	label := "New"
	position := 2
	if _, err := svc.UpdateMedia(context.Background(), "SKU-1", 9, MediaUpdateInput{Label: &label, Position: &position, Types: []string{"image", "thumbnail"}}); err != nil {
		t.Fatal(err)
	}
	if body.Entry.MediaType != "image" || body.Entry.File != "/m/d/demo.png" {
		t.Fatalf("entry = %+v", body.Entry)
	}
}
