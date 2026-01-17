package epub

import (
	"archive/zip"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"

	"github.com/gofrs/uuid/v5"
)

// UnableToCreateEpubError is thrown by Write if it cannot create the destination EPUB file
type UnableToCreateEpubError struct {
	Path string // The path that was given to Write to create the EPUB
	Err  error  // The underlying error that was thrown
}

func (e *UnableToCreateEpubError) Error() string {
	return fmt.Sprintf("Error creating EPUB at %q: %+v", e.Path, e.Err)
}

const (
	containerFilename     = "container.xml"
	containerFileTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles>
    <rootfile full-path="%s/%s" media-type="application/oebps-package+xml" />
  </rootfiles>
</container>
`
	// This seems to be the standard based on the latest EPUB spec:
	// http://www.idpf.org/epub/31/spec/epub-ocf.html
	contentFolderName    = "EPUB"
	coverImageProperties = "cover-image"
	// Permissions for any new directories we create
	dirPermissions = 0755
	// Permissions for any new files we create
	filePermissions   = 0644
	mediaTypeCSS      = "text/css"
	mediaTypeEpub     = "application/epub+zip"
	mediaTypeJpeg     = "image/jpeg"
	mediaTypeNcx      = "application/x-dtbncx+xml"
	mediaTypeXhtml    = "application/xhtml+xml"
	metaInfFolderName = "META-INF"
	mimetypeFilename  = "mimetype"
	pkgFilename       = "package.opf"
	tempDirPrefix     = "go-epub"
	xhtmlFolderName   = "xhtml"
)

// resetWriteState resets the TOC and package manifest/spine so that
// the Epub can be written multiple times without accumulating duplicate entries
func (e *Epub) resetWriteState() error {
	// Reinitialize the TOC to clear any previous entries
	var err error
	e.toc, err = newToc()
	if err != nil {
		return fmt.Errorf("error resetting TOC: %w", err)
	}
	e.toc.setTitle(e.Title())
	e.toc.setIdentifier(e.Identifier())

	// Clear the manifest and spine from the package
	// Keep metadata but reset the file lists
	e.pkg.xml.ManifestItems = nil
	e.pkg.xml.Spine.Items = nil

	return nil
}

// writeContent writes all EPUB content to the specified root directory.
// This is the core logic shared by WriteTo and WriteDirectory.
func (e *Epub) writeContent(rootDir string) error {
	// Reset TOC and package manifest/spine for multiple writes
	// This ensures that calling Write/WriteDirectory multiple times on the same
	// Epub instance doesn't accumulate duplicate entries
	err := e.resetWriteState()
	if err != nil {
		return err
	}

	// The order in which the file is assembled matters for some files.
	// Containers, CSS, media, and sections can only be written after the
	// necessary folders are created (createEPubFolders).
	// The TOC must be written after all sections have been written.
	// The package file is the final file to be written.
	err = writeMimetype(rootDir)
	if err != nil {
		return err
	}
	err = createEpubFolders(rootDir)
	if err != nil {
		return err
	}

	err = writeContainerFile(rootDir)
	if err != nil {
		return err
	}

	err = e.writeCSSFiles(rootDir)
	if err != nil {
		return err
	}

	err = e.writeFonts(rootDir)
	if err != nil {
		return err
	}

	err = e.writeImages(rootDir)
	if err != nil {
		return err
	}

	err = e.writeVideos(rootDir)
	if err != nil {
		return err
	}

	err = e.writeAudios(rootDir)
	if err != nil {
		return err
	}

	e.writeSections(rootDir)
	e.writeToc(rootDir)
	e.writePackageFile(rootDir)
	return nil
}

// WriteTo the dest io.Writer. The return value is the number of bytes written. Any error encountered during the write is also returned.
func (e *Epub) WriteTo(dst io.Writer) (int64, error) {
	e.Lock()
	defer e.Unlock()
	tempDir := uuid.Must(uuid.NewV4()).String()

	err := filesystem.Mkdir(tempDir, dirPermissions)
	if err != nil {
		return 0, fmt.Errorf("Error creating temp directory: %w", err)

	}
	defer func() {
		if err := filesystem.RemoveAll(tempDir); err != nil {
			log.Print("Error removing temp directory: %w", err)
		}
	}()

	err = e.writeContent(tempDir)
	if err != nil {
		return 0, err
	}

	return e.writeEpub(tempDir, dst)
}

// WriteDirectory writes the EPUB as an exploded directory structure for inspection,
// manual modification, and debugging. The destDir must be a path to a directory
// (which will be created if it doesn't exist).
func (e *Epub) WriteDirectory(destDir string) error {
	e.Lock()
	defer e.Unlock()

	// Create a temp directory using the existing filesystem abstraction (like WriteTo does)
	tempDir := uuid.Must(uuid.NewV4()).String()

	err := filesystem.Mkdir(tempDir, dirPermissions)
	if err != nil {
		return fmt.Errorf("Error creating temp directory: %w", err)
	}
	defer func() {
		if err := filesystem.RemoveAll(tempDir); err != nil {
			log.Print("Error removing temp directory: %w", err)
		}
	}()

	// Write EPUB content to temp directory using the existing abstraction
	err = e.writeContent(tempDir)
	if err != nil {
		return err
	}

	// Copy the assembled temp directory to the destination directory
	return copyDirectory(tempDir, destDir)
}

// Write writes the EPUB file. The destination path must be the full path to
// the resulting file, including filename and extension.
// The result is always writen to the local filesystem even if the underlying storage is in memory.
func (e *Epub) Write(destFilePath string) error {

	f, err := os.Create(destFilePath)
	if err != nil {
		return &UnableToCreateEpubError{
			Path: destFilePath,
			Err:  err,
		}
	}
	defer f.Close()
	_, err = e.WriteTo(f)
	return err
}

// copyDirectory recursively copies a directory from the filesystem abstraction to the OS filesystem.
func copyDirectory(srcDir, destDir string) error {
	// Create destination directory
	err := os.MkdirAll(destDir, dirPermissions)
	if err != nil {
		return fmt.Errorf("Error creating destination directory: %w", err)
	}

	// Walk the source directory and copy all files
	return fs.WalkDir(filesystem, srcDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		// Get relative path from source root
		relPath, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}

		destPath := filepath.Join(destDir, relPath)

		if d.IsDir() {
			// Create directory in destination
			return os.MkdirAll(destPath, dirPermissions)
		}

		// Copy file
		srcFile, err := filesystem.Open(path)
		if err != nil {
			return fmt.Errorf("Error opening source file %s: %w", path, err)
		}
		defer srcFile.Close()

		destFile, err := os.Create(destPath)
		if err != nil {
			return fmt.Errorf("Error creating destination file %s: %w", destPath, err)
		}
		defer destFile.Close()

		_, err = io.Copy(destFile, srcFile)
		if err != nil {
			return fmt.Errorf("Error copying file %s: %w", path, err)
		}

		return nil
	})
}

// Create the EPUB folder structure in a temp directory
func createEpubFolders(rootEpubDir string) error {
	if err := filesystem.Mkdir(
		filepath.Join(
			rootEpubDir,
			contentFolderName,
		),
		dirPermissions); err != nil {
		// No reason this should happen if tempDir creation was successful
		return fmt.Errorf("Error creating EPUB subdirectory: %w", err)
	}

	if err := filesystem.Mkdir(
		filepath.Join(
			rootEpubDir,
			contentFolderName,
			xhtmlFolderName,
		),
		dirPermissions); err != nil {
		return fmt.Errorf("Error creating xhtml subdirectory: %w", err)
	}

	if err := filesystem.Mkdir(
		filepath.Join(
			rootEpubDir,
			metaInfFolderName,
		),
		dirPermissions); err != nil {
		return fmt.Errorf("Error creating META-INF subdirectory: %w", err)
	}
	return nil
}

// Write the contatiner file (container.xml), which mostly just points to the
// package file (package.opf)
//
// Spec: http://www.idpf.org/epub/301/spec/epub-ocf.html#sec-container-metainf-container.xml
func writeContainerFile(rootEpubDir string) error {
	containerFilePath := filepath.Join(rootEpubDir, metaInfFolderName, containerFilename)
	if err := filesystem.WriteFile(
		containerFilePath,
		[]byte(
			fmt.Sprintf(
				containerFileTemplate,
				contentFolderName,
				pkgFilename,
			),
		),
		filePermissions,
	); err != nil {
		return fmt.Errorf("Error writing container file: %w", err)
	}
	return nil
}

// Write the CSS files to the temporary directory and add them to the package
// file
func (e *Epub) writeCSSFiles(rootEpubDir string) error {
	err := e.writeMedia(rootEpubDir, e.css, CSSFolderName)
	if err != nil {
		return err
	}

	// Clean up the cover temp file if one was created
	os.Remove(e.cover.cssTempFile)

	return nil
}

// writeCounter counts the number of bytes written to it.
type writeCounter struct {
	Total int64 // Total # of bytes written
}

// Write implements the io.Writer interface.
// Always completes and never returns an error.
func (wc *writeCounter) Write(p []byte) (int, error) {
	n := len(p)
	wc.Total += int64(n)
	return n, nil
}

// AssembleDirectory assembles an exploded EPUB directory into a zipped EPUB file.
// The sourceDir must be a path to a directory containing a valid exploded EPUB structure
// (with mimetype, META-INF/container.xml, and EPUB/ folders).
// The destFilePath must be the full path to the resulting EPUB file, including filename and extension.
func AssembleDirectory(sourceDir, destFilePath string) error {
	// Verify the source directory exists
	info, err := os.Stat(sourceDir)
	if err != nil {
		return fmt.Errorf("Error accessing source directory: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("Source path is not a directory: %s", sourceDir)
	}

	// Verify required files/folders exist
	mimetypePath := filepath.Join(sourceDir, mimetypeFilename)
	if _, err := os.Stat(mimetypePath); os.IsNotExist(err) {
		return fmt.Errorf("Invalid EPUB directory: missing mimetype file")
	}

	containerPath := filepath.Join(sourceDir, metaInfFolderName, containerFilename)
	if _, err := os.Stat(containerPath); os.IsNotExist(err) {
		return fmt.Errorf("Invalid EPUB directory: missing META-INF/container.xml")
	}

	// Create the destination EPUB file
	f, err := os.Create(destFilePath)
	if err != nil {
		return &UnableToCreateEpubError{
			Path: destFilePath,
			Err:  err,
		}
	}
	defer f.Close()

	// Use the writeEpubFromDirectory helper to zip the directory
	_, err = writeEpubFromDirectory(sourceDir, f)
	return err
}

// writeEpubFromDirectory is a helper that zips an exploded EPUB directory to an io.Writer.
// It's similar to writeEpub but works with an existing directory instead of a temp directory.
func writeEpubFromDirectory(rootEpubDir string, dst io.Writer) (int64, error) {
	counter := &writeCounter{}
	teeWriter := io.MultiWriter(counter, dst)

	z := zip.NewWriter(teeWriter)

	skipMimetypeFile := false

	// addFileToZip adds the file present at path to the zip archive
	addFileToZip := func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		// Get the path of the file relative to the folder we're zipping
		relativePath, err := filepath.Rel(rootEpubDir, path)
		if err != nil {
			return err
		}
		relativePath = filepath.ToSlash(relativePath)

		// Only include regular files, not directories
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}

		var w io.Writer
		if filepath.FromSlash(path) == filepath.Join(rootEpubDir, mimetypeFilename) {
			// Skip the mimetype file if it's already been written
			if skipMimetypeFile {
				return nil
			}
			// The mimetype file must be uncompressed according to the EPUB spec
			w, err = z.CreateHeader(&zip.FileHeader{
				Name:   relativePath,
				Method: zip.Store,
			})
		} else {
			w, err = z.Create(relativePath)
		}
		if err != nil {
			return fmt.Errorf("error creating zip writer: %w", err)
		}

		r, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("error opening file %v being added to EPUB: %w", path, err)
		}
		defer func() {
			if err := r.Close(); err != nil {
				log.Println(err)
			}
		}()

		_, err = io.Copy(w, r)
		if err != nil {
			return fmt.Errorf("error copying contents of file being added EPUB: %w", err)
		}
		return nil
	}

	// Add the mimetype file first
	mimetypeFilePath := filepath.Join(rootEpubDir, mimetypeFilename)
	mimetypeInfo, err := os.Stat(mimetypeFilePath)
	if err != nil {
		if err := z.Close(); err != nil {
			log.Println(err)
		}
		return counter.Total, fmt.Errorf("unable to get FileInfo for mimetype file: %w", err)
	}
	err = addFileToZip(mimetypeFilePath, fileInfoToDirEntry(mimetypeInfo), nil)
	if err != nil {
		if err := z.Close(); err != nil {
			log.Println(err)
		}
		return counter.Total, fmt.Errorf("unable to add mimetype file to EPUB: %w", err)
	}

	skipMimetypeFile = true

	err = filepath.WalkDir(rootEpubDir, addFileToZip)
	if err != nil {
		if err := z.Close(); err != nil {
			log.Println(err)
		}
		return counter.Total, fmt.Errorf("unable to add file to EPUB: %w", err)
	}

	err = z.Close()
	return counter.Total, err
}

// Write the EPUB file itself by zipping up everything from a temp directory
// The return value is the number of bytes written. Any error encountered during the write is also returned.
func (e *Epub) writeEpub(rootEpubDir string, dst io.Writer) (int64, error) {
	counter := &writeCounter{}
	teeWriter := io.MultiWriter(counter, dst)

	z := zip.NewWriter(teeWriter)

	skipMimetypeFile := false

	// addFileToZip adds the file present at path to the zip archive. The path is relative to the rootEpubDir
	addFileToZip := func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		// Get the path of the file relative to the folder we're zipping
		relativePath, err := filepath.Rel(rootEpubDir, path)
		if err != nil {
			// tempDir and path are both internal, so we shouldn't get here
			return err
		}
		relativePath = filepath.ToSlash(relativePath)

		// Only include regular files, not directories
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}

		var w io.Writer
		if filepath.FromSlash(path) == filepath.Join(rootEpubDir, mimetypeFilename) {
			// Skip the mimetype file if it's already been written
			if skipMimetypeFile {
				return nil
			}
			// The mimetype file must be uncompressed according to the EPUB spec
			w, err = z.CreateHeader(&zip.FileHeader{
				Name:   relativePath,
				Method: zip.Store,
			})
		} else {
			w, err = z.Create(relativePath)
		}
		if err != nil {
			return fmt.Errorf("error creating zip writer: %w", err)
		}

		r, err := filesystem.Open(path)
		if err != nil {
			return fmt.Errorf("error opening file %v being added to EPUB: %w", path, err)
		}
		defer func() {
			if err := r.Close(); err != nil {
				log.Println(err)
			}
		}()

		_, err = io.Copy(w, r)
		if err != nil {
			return fmt.Errorf("error copying contents of file being added EPUB: %w", err)
		}
		return nil
	}

	// Add the mimetype file first
	mimetypeFilePath := filepath.Join(rootEpubDir, mimetypeFilename)
	mimetypeInfo, err := fs.Stat(filesystem, mimetypeFilePath)
	if err != nil {
		if err := z.Close(); err != nil {
			log.Println(err)
		}
		return counter.Total, fmt.Errorf("unable to get FileInfo for mimetype file: %w", err)
	}
	err = addFileToZip(mimetypeFilePath, fileInfoToDirEntry(mimetypeInfo), nil)
	if err != nil {
		if err := z.Close(); err != nil {
			log.Println(err)
		}
		return counter.Total, fmt.Errorf("unable to add mimetype file to EPUB: %w", err)
	}

	skipMimetypeFile = true

	err = fs.WalkDir(filesystem, rootEpubDir, addFileToZip)
	if err != nil {
		if err := z.Close(); err != nil {
			log.Println(err)
		}
		return counter.Total, fmt.Errorf("unable to add file to EPUB: %w", err)
	}

	err = z.Close()
	return counter.Total, err
}

// Get fonts from their source and save them in the temporary directory
func (e *Epub) writeFonts(rootEpubDir string) error {
	return e.writeMedia(rootEpubDir, e.fonts, FontFolderName)
}

// Get images from their source and save them in the temporary directory
func (e *Epub) writeImages(rootEpubDir string) error {
	return e.writeMedia(rootEpubDir, e.images, ImageFolderName)
}

// Get videos from their source and save them in the temporary directory
func (e *Epub) writeVideos(rootEpubDir string) error {
	return e.writeMedia(rootEpubDir, e.videos, VideoFolderName)
}

// Get audios from their source and save them in the temporary directory
func (e *Epub) writeAudios(rootEpubDir string) error {
	return e.writeMedia(rootEpubDir, e.audios, AudioFolderName)
}

// Get media from their source and save them in the temporary directory
func (e *Epub) writeMedia(rootEpubDir string, mediaMap map[string]string, mediaFolderName string) error {
	if len(mediaMap) > 0 {
		mediaFolderPath := filepath.Join(rootEpubDir, contentFolderName, mediaFolderName)
		if err := filesystem.Mkdir(mediaFolderPath, dirPermissions); err != nil {
			return fmt.Errorf("unable to create directory: %s", err)
		}

		for mediaFilename, mediaSource := range mediaMap {
			mediaType, err := grabber{(e.Client)}.fetchMedia(mediaSource, mediaFolderPath, mediaFilename)
			if err != nil {
				return err
			}
			// The cover image has a special value for the properties attribute
			mediaProperties := ""
			if mediaFilename == e.cover.imageFilename {
				mediaProperties = coverImageProperties
			}

			// Add the file to the OPF manifest
			xmlId, err := fixXMLId(mediaFilename)
			if err != nil {
				return fmt.Errorf("error creating xml id: %w", err)
			}
			e.pkg.addToManifest(xmlId, filepath.Join(mediaFolderName, mediaFilename), mediaType, mediaProperties)
		}
	}
	return nil
}

// fixXMLId takes a string and returns an XML id compatible string.
// https://www.w3.org/TR/REC-xml-names/#NT-NCName
// This means it must not contain a colon (:) or whitespace and it must not
// start with a digit, punctuation or diacritics
func fixXMLId(id string) (string, error) {
	if len(id) == 0 {
		return "", fmt.Errorf("no id given")
	}
	namespace := uuid.NewV5(uuid.NamespaceURL, "github.com/go-shiori/go-epub")
	fileIdentifier := fmt.Sprintf("id%s", uuid.NewV5(namespace, id))
	return fileIdentifier, nil
}

// Write the mimetype file
//
// Spec: http://www.idpf.org/epub/301/spec/epub-ocf.html#sec-zip-container-mime
func writeMimetype(rootEpubDir string) error {
	mimetypeFilePath := filepath.Join(rootEpubDir, mimetypeFilename)

	if err := filesystem.WriteFile(mimetypeFilePath, []byte(mediaTypeEpub), filePermissions); err != nil {
		return fmt.Errorf("Error writing mimetype file: %w", err)
	}
	return nil
}

func (e *Epub) writePackageFile(rootEpubDir string) {
	err := e.pkg.write(rootEpubDir)
	if err != nil {
		log.Println(err)
	}
}

// Write the section files to the temporary directory and add the sections to
// the TOC and package files
func (e *Epub) writeSections(rootEpubDir string) {
	filenamelist := getFilenames(e.sections)
	parentlist := getParents(e.sections, "-1")
	if len(e.sections) > 0 {
		// If a cover was set, add it to the package spine first so it shows up
		// first in the reading order
		if e.cover.xhtmlFilename != "" {
			e.pkg.addToSpine(e.cover.xhtmlFilename)
		}
		err := writeSections(rootEpubDir, e, e.sections, parentlist, filenamelist)
		if err != nil {
			log.Println(err)
		}
	}
}

// Write the TOC file to the temporary directory and add the TOC entries to the
// package file. All `writeSections` calls must be done before this.
func (e *Epub) writeToc(rootEpubDir string) {
	e.pkg.addToManifest(tocNavItemID, tocNavFilename, mediaTypeXhtml, tocNavItemProperties)
	e.pkg.addToManifest(tocNcxItemID, tocNcxFilename, mediaTypeNcx, "")

	err := e.toc.write(rootEpubDir)
	if err != nil {
		log.Println(err)
	}

}

// Create a list of sections and their parents.
// -1 means that sections are appended to the root (have no parents), like section and cover.
func getParents(sections []*epubSection, root string) map[string]string {
	fileparent := make(map[string]string)
	for _, section := range sections {
		fileparent[section.filename] = root
		if section.children != nil {
			childFileparent := getParents(section.children, section.filename)
			for filename, parent := range childFileparent {
				fileparent[filename] = parent
			}
		}
	}
	return fileparent
}

func writeSections(rootEpubDir string, e *Epub, sections []*epubSection, parentfilename map[string]string, filenamelist map[string]int) error {
	for _, section := range sections {

		// Set the title of the cover page XHTML to the title of the EPUB
		if section.filename == e.cover.xhtmlFilename {
			section.xhtml.setTitle(e.Title())
		}

		sectionFilePath := filepath.Join(rootEpubDir, contentFolderName, xhtmlFolderName, section.filename)
		err := section.xhtml.write(sectionFilePath)
		if err != nil {
			log.Println(err)
		}

		relativePath := filepath.Join(xhtmlFolderName, section.filename)
		if section.filename != e.cover.xhtmlFilename {
			e.pkg.addToSpine(section.filename)
		}
		e.pkg.addToManifest(section.filename, relativePath, mediaTypeXhtml, "")
		if parentfilename[section.filename] == "-1" && section.filename != e.cover.xhtmlFilename {
			j := filenamelist[section.filename]
			e.toc.addSubSection("-1", j, section.xhtml.Title(), relativePath)
		}
		if parentfilename[section.filename] != "-1" && section.filename != e.cover.xhtmlFilename {
			j := filenamelist[section.filename]
			parentfilenameis := parentfilename[section.filename]
			e.toc.addSubSection(parentfilenameis, j, section.xhtml.Title(), relativePath)
		}
		if section.children != nil {
			err = writeSections(rootEpubDir, e, section.children, parentfilename, filenamelist)
			if err != nil {
				log.Println(err)
			}
		}
	}

	return nil
}
