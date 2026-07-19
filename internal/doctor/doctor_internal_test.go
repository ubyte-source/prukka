package doctor

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDistributedEngineManifestMatchesDoctorContract(t *testing.T) {
	t.Parallel()

	path := filepath.Join("..", "..", "engine", engineManifestName)
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatal(err)
	}
	if err := decodeEngineManifest(data); err != nil {
		t.Fatalf("distributed engine manifest: %v", err)
	}
}
