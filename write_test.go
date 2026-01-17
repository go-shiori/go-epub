package epub

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestEpubWriteTo(t *testing.T) {
	e, err := NewEpub(testEpubTitle)
	if err != nil {
		t.Error(err)
	}
	var b bytes.Buffer
	n, err := e.WriteTo(&b)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(b.Bytes())) != n {
		t.Fatalf("Expected size %v, got %v", len(b.Bytes()), n)
	}
}

func TestWriteToErrors(t *testing.T) {
	t.Run("CSS", func(t *testing.T) {
		e, err := NewEpub(testEpubTitle)
		if err != nil {
			t.Error(err)
		}
		testWriteToErrors(t, e, e.AddCSS, "cover.css")
	})
	t.Run("Font", func(t *testing.T) {
		e, err := NewEpub(testEpubTitle)
		if err != nil {
			t.Error(err)
		}
		testWriteToErrors(t, e, e.AddFont, "redacted-script-regular.ttf")
	})
	t.Run("Image", func(t *testing.T) {
		e, err := NewEpub(testEpubTitle)
		if err != nil {
			t.Error(err)
		}
		testWriteToErrors(t, e, e.AddImage, "gophercolor16x16.png")
	})
	t.Run("Video", func(t *testing.T) {
		e, err := NewEpub(testEpubTitle)
		if err != nil {
			t.Error(err)
		}
		testWriteToErrors(t, e, e.AddVideo, "sample_640x360.mp4")
	})
	t.Run("Audio", func(t *testing.T) {
		e, err := NewEpub(testEpubTitle)
		if err != nil {
			t.Error(err)
		}
		testWriteToErrors(t, e, e.AddAudio, "sample_audio.wav")
	})
}

func testWriteToErrors(t *testing.T, e *Epub, adder func(string, string) (string, error), name string) {
	// Copy testdata to temp file
	data, err := os.Open(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("cannot open testdata: %v", err)
	}
	defer data.Close()
	temp, err := os.CreateTemp("", "temp")
	if err != nil {
		t.Fatalf("unable to create temp file: %v", err)
	}
	_, err = io.Copy(temp, data)
	if err != nil {
		t.Fatalf("unable to copy tmp file to destination: %v", err)
	}

	temp.Close()
	// Add temp file to epub
	if _, err := adder(temp.Name(), ""); err != nil {
		t.Fatalf("unable to add temp file: %v", err)
	}
	// Delete temp file
	if err := os.Remove(temp.Name()); err != nil {
		t.Fatalf("unable to delete temp file: %v", err)
	}
	// Write epub to buffer
	var b bytes.Buffer
	if _, err := e.WriteTo(&b); err == nil {
		t.Fatal("Expected error")
	}
}

func TestWriteDirectory(t *testing.T) {
	e, err := NewEpub("Test WriteDirectory")
	if err != nil {
		t.Fatal(err)
	}

	e.SetAuthor("Test Author")
	_, err = e.AddSection("<h1>Chapter 1</h1><p>Content here.</p>", "Chapter 1", "", "")
	if err != nil {
		t.Fatal(err)
	}

	tempDir := t.TempDir()
	println(tempDir)
	epubDir := filepath.Join(tempDir, "test-epub")

	err = e.WriteDirectory(epubDir)
	if err != nil {
		t.Fatal(err)
	}

	// Verify the directory structure was created
	expectedPaths := []string{
		filepath.Join(epubDir, "mimetype"),
		filepath.Join(epubDir, "META-INF", "container.xml"),
		filepath.Join(epubDir, "EPUB", "package.opf"),
		filepath.Join(epubDir, "EPUB", "xhtml"),
	}

	for _, path := range expectedPaths {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			t.Errorf("Expected path does not exist: %s", path)
		}
	}
}

func TestAssembleDirectory(t *testing.T) {
	// First create an exploded EPUB directory
	e, err := NewEpub("Test AssembleDirectory")
	if err != nil {
		t.Fatal(err)
	}

	e.SetAuthor("Test Author")
	_, err = e.AddSection("<h1>Chapter 1</h1><p>Content here.</p>", "Chapter 1", "", "")
	if err != nil {
		t.Fatal(err)
	}

	tempDir := t.TempDir()
	epubDir := filepath.Join(tempDir, "test-epub")

	err = e.WriteDirectory(epubDir)
	if err != nil {
		t.Fatal(err)
	}

	// Now assemble it into a zipped EPUB
	epubFile := filepath.Join(tempDir, "test.epub")
	err = AssembleDirectory(epubDir, epubFile)
	if err != nil {
		t.Fatal(err)
	}

	// Verify the EPUB file was created
	info, err := os.Stat(epubFile)
	if err != nil {
		t.Fatalf("EPUB file was not created: %v", err)
	}

	if info.Size() == 0 {
		t.Error("EPUB file is empty")
	}
}

func TestAssembleDirectoryErrors(t *testing.T) {
	tempDir := t.TempDir()

	t.Run("NonExistentDirectory", func(t *testing.T) {
		err := AssembleDirectory(filepath.Join(tempDir, "nonexistent"), filepath.Join(tempDir, "test.epub"))
		if err == nil {
			t.Error("Expected error for non-existent directory")
		}
	})

	t.Run("NotADirectory", func(t *testing.T) {
		// Create a file instead of a directory
		filePath := filepath.Join(tempDir, "notadir")
		if err := os.WriteFile(filePath, []byte("test"), 0644); err != nil {
			t.Fatal(err)
		}

		err := AssembleDirectory(filePath, filepath.Join(tempDir, "test.epub"))
		if err == nil {
			t.Error("Expected error for file instead of directory")
		}
	})

	t.Run("MissingMimetype", func(t *testing.T) {
		invalidDir := filepath.Join(tempDir, "invalid-epub")
		if err := os.MkdirAll(invalidDir, 0755); err != nil {
			t.Fatal(err)
		}

		err := AssembleDirectory(invalidDir, filepath.Join(tempDir, "test.epub"))
		if err == nil {
			t.Error("Expected error for missing mimetype file")
		}
	})

	t.Run("MissingContainerXML", func(t *testing.T) {
		invalidDir := filepath.Join(tempDir, "invalid-epub2")
		if err := os.MkdirAll(invalidDir, 0755); err != nil {
			t.Fatal(err)
		}
		// Create mimetype but not container.xml
		if err := os.WriteFile(filepath.Join(invalidDir, "mimetype"), []byte("application/epub+zip"), 0644); err != nil {
			t.Fatal(err)
		}

		err := AssembleDirectory(invalidDir, filepath.Join(tempDir, "test.epub"))
		if err == nil {
			t.Error("Expected error for missing container.xml")
		}
	})
}

func TestRoundTripDirectoryToZip(t *testing.T) {
	// Create an EPUB, write it to a directory, assemble it, and verify it's valid
	e, err := NewEpub("Test RoundTrip")
	if err != nil {
		t.Fatal(err)
	}

	e.SetAuthor("Test Author")
	e.SetDescription("Test Description")
	_, err = e.AddSection("<h1>Chapter 1</h1><p>First chapter.</p>", "Chapter 1", "", "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.AddSection("<h1>Chapter 2</h1><p>Second chapter.</p>", "Chapter 2", "", "")
	if err != nil {
		t.Fatal(err)
	}

	//tempDir := t.TempDir()

	tempDir, err := os.MkdirTemp("", "test-*")
	if err != nil {
		t.Fatal(err)
	}

	// Write as directory
	epubDir := filepath.Join(tempDir, "test-epub")
	err = e.WriteDirectory(epubDir)
	if err != nil {
		t.Fatal(err)
	}

	// Assemble to zip
	epubFile := filepath.Join(tempDir, "test.epub")
	err = AssembleDirectory(epubDir, epubFile)
	if err != nil {
		t.Fatal(err)
	}

	// Write directly to zip for comparison
	epubFile2 := filepath.Join(tempDir, "test2.epub")
	err = e.Write(epubFile2)
	if err != nil {
		t.Fatal(err)
	}

	// Both files should be identical in content
	// Compare zip file contents rather than raw bytes because
	// timestamps and compression details may vary slightly
	err = compareEpubFiles(epubFile, epubFile2)
	if err != nil {
		t.Error(err)
	}

}

// compareEpubFiles compares two EPUB files by extracting and comparing their contents
func compareEpubFiles(file1, file2 string) error {
	z1, err := zip.OpenReader(file1)
	if err != nil {
		return fmt.Errorf("error opening %s: %w", file1, err)
	}
	defer z1.Close()

	z2, err := zip.OpenReader(file2)
	if err != nil {
		return fmt.Errorf("error opening %s: %w", file2, err)
	}
	defer z2.Close()

	if len(z1.File) != len(z2.File) {
		return fmt.Errorf("different number of files: %d vs %d", len(z1.File), len(z2.File))
	}

	// Create maps of filename -> content for both zips
	files1 := make(map[string][]byte)
	for _, f := range z1.File {
		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("error opening %s in first zip: %w", f.Name, err)
		}
		content, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return fmt.Errorf("error reading %s in first zip: %w", f.Name, err)
		}
		files1[f.Name] = content
	}

	files2 := make(map[string][]byte)
	for _, f := range z2.File {
		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("error opening %s in second zip: %w", f.Name, err)
		}
		content, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return fmt.Errorf("error reading %s in second zip: %w", f.Name, err)
		}
		files2[f.Name] = content
	}

	// Compare file contents
	for name, content1 := range files1 {
		content2, ok := files2[name]
		if !ok {
			return fmt.Errorf("file %s exists in first epub but not in second", name)
		}
		if !bytes.Equal(content1, content2) {
			return fmt.Errorf("file %s has different content: %d bytes vs %d bytes", name, len(content1), len(content2))
		}
	}

	return nil
}
