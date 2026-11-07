package egress

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	domain "github.com/chenyme/grok2api/backend/internal/domain/egress"
	"github.com/chenyme/grok2api/backend/internal/infra/security"
)

func TestIsNonPublicLiteralProxyCoversEveryProxyFamily(t *testing.T) {
	vmess := func(address string) string {
		return "vmess://" + base64.RawStdEncoding.EncodeToString([]byte(`{"v":"2","add":"`+address+`","port":"443","id":"123e4567-e89b-12d3-a456-426614174000","aid":"0","scy":"auto","net":"tcp"}`))
	}
	tests := map[string]bool{
		"http://127.0.0.1:8080":     true,
		"socks5://10.0.0.2:1080":    true,
		"socks5h://[::1]:1080":      true,
		"socks4://192.168.0.9:1080": true,
		"http://169.254.169.254:80": true,
		"trojan://password@192.168.1.10:443?security=tls&sni=edge.example": true,
		"ss://YWVzLTEyOC1nY206c2VjcmV0@100.64.0.1:8388":                    true,
		vmess("172.16.0.5"):            true,
		"http://1.1.1.1:8080":          false,
		"socks5h://proxy.example:1080": false,
		"trojan://password@8.8.8.8:443?security=tls&sni=edge.example": false,
		vmess("9.9.9.9"): false,
	}
	for raw, want := range tests {
		normalized, err := NormalizeProxyURL(raw)
		if err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		if got := isNonPublicLiteralProxy(normalized); got != want {
			t.Fatalf("isNonPublicLiteralProxy(%s) = %v, want %v", raw, got, want)
		}
	}
}

func TestSyncSourceSkipsNonPublicLiteralNodes(t *testing.T) {
	cipher, err := security.NewCipher("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	if err != nil {
		t.Fatal(err)
	}
	proxy := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(strings.Join([]string{
			"http://10.0.0.8:8080",
			"socks5h://127.0.0.1:1080",
			"http://public-node.example:8080",
			"trojan://password@8.8.8.8:443?security=tls&sni=edge.example",
		}, "\n")))
	}))
	defer proxy.Close()
	encryptedSourceURL, err := cipher.Encrypt("http://1.1.1.1/subscription")
	if err != nil {
		t.Fatal(err)
	}
	encryptedProxyURL, err := cipher.Encrypt(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	repository := &subscriptionSyncRepositoryStub{nodes: make(map[uint64][]domain.Node)}
	service := &Service{cipher: cipher}
	result, err := service.syncSource(context.Background(), repository, domain.SubscriptionSource{
		ID: 1, Name: "source", Scope: domain.ScopeBuild, Enabled: true,
		EncryptedURL: encryptedSourceURL, EncryptedProxyURL: encryptedProxyURL, RefreshIntervalSeconds: 900,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Imported != 2 || result.NonPublic != 2 || result.Skipped != 2 {
		t.Fatalf("sync result = %#v", result)
	}
	for _, node := range repository.nodesFor(1) {
		proxyURL, err := cipher.Decrypt(node.EncryptedProxyURL)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(proxyURL, "10.0.0.8") || strings.Contains(proxyURL, "127.0.0.1") {
			t.Fatalf("remote subscription imported a non-public node: %s", proxyURL)
		}
	}
}

type importTextRepositoryStub struct {
	OperationsRepository
	created []domain.Node
}

func (r *importTextRepositoryStub) CreateEgressNodes(_ context.Context, nodes []domain.Node) (int, error) {
	r.created = append(r.created, nodes...)
	return len(nodes), nil
}

func TestManualImportKeepsNonPublicLiteralNodes(t *testing.T) {
	cipher, err := security.NewCipher("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	if err != nil {
		t.Fatal(err)
	}
	repository := &importTextRepositoryStub{}
	service := &Service{cipher: cipher, operations: repository}
	result, err := service.ImportText(context.Background(), ImportInput{
		Name: "manual", Scope: domain.ScopeBuild, Content: "http://10.0.0.8:8080\nsocks5h://127.0.0.1:1080\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Imported != 2 || result.NonPublic != 0 || len(repository.created) != 2 {
		t.Fatalf("manual import result = %#v created=%d", result, len(repository.created))
	}
}
