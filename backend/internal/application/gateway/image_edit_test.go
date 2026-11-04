package gateway

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"
)

func TestResolveImageInputFileReferenceToDataURI(t *testing.T) {
	raw := []byte("png-bytes")
	inputID := "input_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	store := &videoAssetStoreStub{inputID: inputID, inputData: raw}
	service := &Service{mediaAssets: store}
	reference := ImageInputFileReference(inputID)
	if reference != "grok2api-input:"+inputID {
		t.Fatalf("reference = %q", reference)
	}
	resolved, err := service.resolveImageInputReferences(context.Background(), []string{"https://example.com/a.png", reference})
	if err != nil {
		t.Fatal(err)
	}
	want := "data:image/png;base64," + base64.StdEncoding.EncodeToString(raw)
	if len(resolved) != 2 || resolved[0] != "https://example.com/a.png" || resolved[1] != want {
		t.Fatalf("resolved = %#v", resolved)
	}
	if _, err := service.resolveImageInputReferences(context.Background(), []string{ImageInputFileReference("missing")}); !errors.Is(err, ErrImageInputUnavailable) {
		t.Fatalf("missing input error = %v", err)
	}
	store.inputKind = "video"
	if _, err := service.resolveImageInputReferences(context.Background(), []string{reference}); !errors.Is(err, ErrImageInputUnavailable) {
		t.Fatalf("video input accepted for image edit: %v", err)
	}
	store.inputKind = ""
	store.inputSize = 40 << 20
	if _, err := service.resolveImageInputReferences(context.Background(), []string{reference}); !errors.Is(err, ErrImageInputTooLarge) {
		t.Fatalf("oversized local input error = %v", err)
	}
}
