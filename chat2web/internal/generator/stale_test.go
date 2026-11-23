package generator

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCurrentTopicAndFindStaleText(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "src", "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "src", "pages"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "data", "site.ts"),
		[]byte("export const siteName = '北京美食指南'\nexport const topic = '北京美食'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "pages", "Home.tsx"),
		[]byte("export default function Home() {\n  return <h1>北京美食</h1>\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "pages", "Blank.css"),
		[]byte("/* 北京美食 */\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, "index.html"),
		[]byte("<html>\n<title>北京美食探索指南</title>\n</html>\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := CurrentTopic(dir); got != "北京美食" {
		t.Fatalf("CurrentTopic = %q", got)
	}
	hits := FindStaleText(dir, "北京美食", 10)
	if len(hits) != 4 {
		t.Fatalf("hits = %d, want 4: %v", len(hits), hits)
	}
	if hits[1].File != "src/data/site.ts" || hits[1].Line != 2 {
		t.Fatalf("second hit = %+v", hits[1])
	}
	if hits[2].File != "src/pages/Home.tsx" || hits[2].Line != 2 {
		t.Fatalf("third hit = %+v", hits[2])
	}
	if hits[3].File != "index.html" || hits[3].Line != 2 {
		t.Fatalf("fourth hit = %+v", hits[3])
	}
	if got := FindStaleText(dir, "涮羊肉", 10); len(got) != 0 {
		t.Fatalf("unexpected hits for new topic: %v", got)
	}
}

func TestCurrentTopicMissing(t *testing.T) {
	if got := CurrentTopic(t.TempDir()); got != "" {
		t.Fatalf("CurrentTopic = %q, want empty", got)
	}
}
