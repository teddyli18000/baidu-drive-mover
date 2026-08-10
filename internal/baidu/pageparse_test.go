package baidu

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

func TestExtractShareContextFromEmbeddedShareBootstrap(t *testing.T) {
	embedded := `locals.mset({"loginstate":1,"bdstoken":"token","shareid":"123","share_uk":"456","uk":"789"});`
	outer, err := json.Marshal(map[string]any{
		"locals": map[string]any{"share": []string{embedded}},
	})
	if err != nil {
		t.Fatal(err)
	}
	page := []byte(`<html><script>window.yunData=` + string(outer) + `;</script></html>`)

	ctx, err := extractShareContext(page)
	if err != nil {
		t.Fatal(err)
	}
	if ctx.BDSToken != "token" || ctx.ShareID != "123" || ctx.ShareUK != "456" || ctx.UK != "789" {
		t.Fatalf("unexpected embedded share context: %+v", ctx)
	}
}

func TestExtractShareContextChecksPastDecoyMarker(t *testing.T) {
	page := []byte(`<script>boot({"loginstate":0});ready({"loginstate":1,"bdstoken":"token","shareid":123,"share_uk":456});</script>`)
	ctx, err := extractShareContext(page)
	if err != nil {
		t.Fatal(err)
	}
	if ctx.ShareID != "123" || ctx.ShareUK != "456" {
		t.Fatalf("unexpected share context after decoy: %+v", ctx)
	}
}

func TestExtractShareContextDoesNotCombineSeparateObjects(t *testing.T) {
	page := []byte(`<script>a({"loginstate":1,"bdstoken":"token"});b({"shareid":123,"share_uk":456});</script>`)
	if _, err := extractShareContext(page); !errors.Is(err, ErrAuthRequired) {
		t.Fatalf("expected split metadata to fail closed, got %v", err)
	}
}

func TestExtractShareContextBoundsEmbeddedDepth(t *testing.T) {
	value := `boot({"loginstate":1,"bdstoken":"token","shareid":123,"share_uk":456});`
	for i := 0; i <= maxEmbeddedShareContextDepth; i++ {
		encoded, err := json.Marshal(map[string]string{"wrapper": value})
		if err != nil {
			t.Fatal(err)
		}
		value = fmt.Sprintf("wrap(%s)", encoded)
	}
	if _, err := extractShareContext([]byte(value)); !errors.Is(err, ErrAuthRequired) {
		t.Fatalf("expected over-deep embedded metadata to fail closed, got %v", err)
	}
}
