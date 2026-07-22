package p2p

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	libp2pcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

func writeP2PKey(t *testing.T) (string, peer.ID) {
	t.Helper()
	key, _, err := libp2pcrypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := libp2pcrypto.MarshalPrivateKey(key)
	path := filepath.Join(t.TempDir(), "p2p-private-key")
	if err := os.WriteFile(path, []byte(base64.StdEncoding.EncodeToString(raw)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	id, _ := peer.IDFromPrivateKey(key)
	return path, id
}

func TestLoadPersistentEd25519Identity(t *testing.T) {
	path, want := writeP2PKey(t)
	key, got, err := LoadPersistentEd25519Identity(path)
	if err != nil {
		t.Fatal(err)
	}
	if key == nil || got != want {
		t.Fatalf("identity = %v, %s; want %s", key, got, want)
	}
	secp, _, _ := libp2pcrypto.GenerateSecp256k1Key(rand.Reader)
	raw, _ := libp2pcrypto.MarshalPrivateKey(secp)
	if err := os.WriteFile(path, []byte(base64.StdEncoding.EncodeToString(raw)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadPersistentEd25519Identity(path); err == nil {
		t.Fatal("non-Ed25519 identity accepted")
	}
}

func TestLoadBootstrapPeersStrictlyBindsPeerAndMultiaddr(t *testing.T) {
	_, self := writeP2PKey(t)
	_, remote := writeP2PKey(t)
	path := filepath.Join(t.TempDir(), "bootstrap.json")
	body := fmt.Sprintf(`{"version":1,"nodes":[{"name":"self","peer_id":%q,"multiaddr":%q},{"name":"remote","peer_id":%q,"multiaddr":%q}]}`,
		self.String(), "/ip4/127.0.0.1/tcp/4001/p2p/"+self.String(), remote.String(), "/ip4/127.0.0.1/tcp/4002/p2p/"+remote.String())
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	peers, err := LoadBootstrapPeers(path, self)
	if err != nil {
		t.Fatal(err)
	}
	if len(peers) != 1 || peers[0].ID != remote {
		t.Fatalf("peers = %+v", peers)
	}
	bad := fmt.Sprintf(`{"version":1,"nodes":[{"name":"remote","peer_id":%q,"multiaddr":%q}],"extra":true}`, remote.String(), "/ip4/127.0.0.1/tcp/4002/p2p/"+self.String())
	if err := os.WriteFile(path, []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadBootstrapPeers(path, self); err == nil {
		t.Fatal("unknown field/wrong peer binding accepted")
	}
}

func TestDependencyEvidenceTopicIsVersioned(t *testing.T) {
	if DependencyEvidenceTopic != "/trustmap/dependency-evidence/1" {
		t.Fatalf("topic = %q", DependencyEvidenceTopic)
	}
}
