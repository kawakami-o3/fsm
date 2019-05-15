package main

import (
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/cbroglie/mustache"
	"github.com/rakyll/statik/fs"

	_ "github.com/kawakami-o3/souko/statik"
)

var root = http.Dir(".")

const TimeFormat = "Mon, 02 Jan 2006 15:04:05 GMT"
const sniffLen = 512

var errNoOverlap = errors.New("invalid range: failed to overlap")

type condResult int

const filesUrlTop = "/files"

const (
	condNone condResult = iota
	condTrue
	condFalse
)

type httpRange struct {
	start, length int64
}

type countingWriter int64

func (w *countingWriter) Write(p []byte) (n int, err error) {
	*w += countingWriter(len(p))
	return len(p), nil
}

func isDirPath(s string) bool {
	return s[len(s)-1] == '/'
}

func redirect(w http.ResponseWriter, req *http.Request, newPath string) {
	w.Header().Set("Location", newPath)
	w.WriteHeader(http.StatusMovedPermanently)
}

func router(w http.ResponseWriter, req *http.Request) {
	targetPath := req.URL.Path
	fmt.Println(targetPath)

	if strings.Index(targetPath, filesUrlTop) == 0 {
		handleFiles(w, req)
	} else if strings.Index(targetPath, "/upload") == 0 {
		// upload
		handleUpload(w, req)
	} else {
		redirect(w, req, "/files/")
	}
}

func handleFiles(w http.ResponseWriter, req *http.Request) {

	//targetPath := path.Clean(req.URL.Path)
	targetPath := req.URL.Path[len(filesUrlTop):]

	f, err := root.Open(targetPath)
	if err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	defer f.Close()

	s, err := f.Stat()
	if err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}

	// redirect
	if s.IsDir() {
		if !isDirPath(targetPath) {
			redirect(w, req, targetPath+"/")
			return
		}
	} else {
		if isDirPath(targetPath) {
			redirect(w, req, targetPath[:len(targetPath)-1])
			return
		}
	}

	if s.IsDir() {
		files, _ := ioutil.ReadDir("./" + targetPath) // TODO error handling

		entries := []map[string]string{}
		for _, f := range files {
			entries = append(entries, map[string]string{
				"url":  fmt.Sprintf("%s%s", req.RequestURI, f.Name()),
				"name": f.Name(),
			})
		}

		layout, err := loadTemplate("/index.html")
		if err != nil {
			// TODO error
			return
		}

		html, err := mustache.Render(layout, map[string][]map[string]string{
			"files": entries,
		})
		if err != nil {
			// TODO error
			return
		}
		io.WriteString(w, html)
	} else {
		serveFile(w, req, s)
	}
}

func loadTemplate(s string) (string, error) {
	statikFS, err := fs.New()
	if err != nil {
		return "", err
	}

	file, err := statikFS.Open(s)
	if err != nil {
		return "", err
	}

	body, err := ioutil.ReadAll(file)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// from net/http in golang
func scanETag(s string) (etag string, remain string) {
	s = textproto.TrimString(s)
	start := 0
	if strings.HasPrefix(s, "W/") {
		start = 2
	}
	if len(s[start:]) < 2 || s[start] != '"' {
		return "", ""
	}
	// ETag is either W/"text" or "text".
	// See RFC 7232 2.3.
	for i := start + 1; i < len(s); i++ {
		c := s[i]
		switch {
		// Character values allowed in ETags.
		case c == 0x21 || c >= 0x23 && c <= 0x7E || c >= 0x80:
		case c == '"':
			return s[:i+1], s[i+1:]
		default:
			return "", ""
		}
	}
	return "", ""
}

func extract(h http.Header, key string) string {
	if v := h[key]; len(v) > 0 {
		return v[0]
	}
	return ""
}

func isZeroTime(t time.Time) bool {
	return t.IsZero() || t.Equal(unixEpochTime)
}

var unixEpochTime = time.Unix(0, 0)

var timeFormats = []string{
	TimeFormat,
	time.RFC850,
	time.ANSIC,
}

func ParseTime(text string) (t time.Time, err error) {
	for _, layout := range timeFormats {
		t, err = time.Parse(layout, text)
		if err == nil {
			return
		}
	}
	return
}

// from net/http in golang
func etagStrongMatch(a, b string) bool {
	return a == b && a != "" && a[0] == '"'
}

// from net/http in golang
func etagWeakMatch(a, b string) bool {
	return strings.TrimPrefix(a, "W/") == strings.TrimPrefix(b, "W/")
}

// from net/http in golang
func writeNotModified(w http.ResponseWriter) {
	h := w.Header()
	delete(h, "Content-Type")
	delete(h, "Content-Length")
	if h.Get("Etag") != "" {
		delete(h, "Last-Modified")
	}
	w.WriteHeader(http.StatusNotModified)
}

// from net/http in golang
func checkIfRange(w http.ResponseWriter, r *http.Request, modtime time.Time) condResult {
	if r.Method != "GET" && r.Method != "HEAD" {
		return condNone
	}
	ir := extract(r.Header, "If-Range")
	if ir == "" {
		return condNone
	}
	etag, _ := scanETag(ir)
	if etag != "" {
		if etagStrongMatch(etag, w.Header().Get("Etag")) {
			return condTrue
		} else {
			return condFalse
		}
	}
	// The If-Range value is typically the ETag value, but it may also be
	// the modtime date. See golang.org/issue/8367.
	if modtime.IsZero() {
		return condFalse
	}
	t, err := ParseTime(ir)
	if err != nil {
		return condFalse
	}
	if t.Unix() == modtime.Unix() {
		return condTrue
	}
	return condFalse
}

// from net/http in golang
func checkIfModifiedSince(r *http.Request, modtime time.Time) condResult {
	if r.Method != "GET" && r.Method != "HEAD" {
		return condNone
	}
	ims := r.Header.Get("If-Modified-Since")
	if ims == "" || isZeroTime(modtime) {
		return condNone
	}
	t, err := ParseTime(ims)
	if err != nil {
		return condNone
	}
	if modtime.Before(t.Add(1 * time.Second)) {
		return condFalse
	}
	return condTrue
}

// from net/http in golang
func checkIfNoneMatch(w http.ResponseWriter, r *http.Request) condResult {
	inm := extract(r.Header, "If-None-Match")
	if inm == "" {
		return condNone
	}
	buf := inm
	for {
		buf = textproto.TrimString(buf)
		if len(buf) == 0 {
			break
		}
		if buf[0] == ',' {
			buf = buf[1:]
		}
		if buf[0] == '*' {
			return condFalse
		}
		etag, remain := scanETag(buf)
		if etag == "" {
			break
		}
		if etagWeakMatch(etag, extract(w.Header(), "Etag")) {
			return condFalse
		}
		buf = remain
	}
	return condTrue
}

// from net/http in golang
func checkIfMatch(w http.ResponseWriter, r *http.Request) condResult {
	im := r.Header.Get("If-Match")
	if im == "" {
		return condNone
	}
	for {
		im = textproto.TrimString(im)
		if len(im) == 0 {
			break
		}
		if im[0] == ',' {
			im = im[1:]
			continue
		}
		if im[0] == '*' {
			return condTrue
		}
		etag, remain := scanETag(im)
		if etag == "" {
			break
		}
		if etagStrongMatch(etag, extract(w.Header(), "Etag")) {
			return condTrue
		}
		im = remain
	}

	return condFalse
}

func checkIfUnmodifiedSince(r *http.Request, modtime time.Time) condResult {
	ius := r.Header.Get("If-Unmodified-Since")
	if ius == "" || isZeroTime(modtime) {
		return condNone
	}
	if t, err := ParseTime(ius); err == nil {
		// The Date-Modified header truncates sub-second precision, so
		// use mtime < t+1s instead of mtime <= t to check for unmodified.
		if modtime.Before(t.Add(1 * time.Second)) {
			return condTrue
		}
		return condFalse
	}
	return condNone
}

// from net/http in golang
func checkPreconditions(w http.ResponseWriter, r *http.Request, modtime time.Time) (done bool, rangeHeader string) {
	// This function carefully follows RFC 7232 section 6.
	ch := checkIfMatch(w, r)
	if ch == condNone {
		ch = checkIfUnmodifiedSince(r, modtime)
	}
	if ch == condFalse {
		w.WriteHeader(http.StatusPreconditionFailed)
		return true, ""
	}
	switch checkIfNoneMatch(w, r) {
	case condFalse:
		if r.Method == "GET" || r.Method == "HEAD" {
			writeNotModified(w)
			return true, ""
		} else {
			w.WriteHeader(http.StatusPreconditionFailed)
			return true, ""
		}
	case condNone:
		if checkIfModifiedSince(r, modtime) == condFalse {
			writeNotModified(w)
			return true, ""
		}
	}

	rangeHeader = extract(r.Header, "Range")
	if rangeHeader != "" && checkIfRange(w, r, modtime) == condFalse {
		rangeHeader = ""
	}
	return false, rangeHeader
}

func serveFile(w http.ResponseWriter, req *http.Request, s os.FileInfo) {
	//sizeFunc := func() (int64, error) { return s.Size(), nil }

	modtime := s.ModTime()
	w.Header().Set("Last-Modified", modtime.UTC().Format(TimeFormat))

	done, rangeReq := checkPreconditions(w, req, modtime)
	if done {
		return
	}

	name := s.Name()
	content, err := root.Open(name)
	if err != nil {
		// TODO error
		return
	}

	code := http.StatusOK

	ctypes, haveType := w.Header()["Content-Type"]
	var ctype string
	if !haveType {
		ctype = mime.TypeByExtension(filepath.Ext(name))
		if ctype == "" {
			// read a chunk to decide between utf-8 text and binary
			var buf [sniffLen]byte
			n, _ := io.ReadFull(content, buf[:])
			ctype = http.DetectContentType(buf[:n])
			_, err := content.Seek(0, io.SeekStart) // rewind to output whole file
			if err != nil {
				// TODO error
				return
			}
		}
		w.Header().Set("Content-Type", ctype)
	} else if len(ctypes) > 0 {
		ctype = ctypes[0]
	}

	size := s.Size()

	// handle Content-Range header.
	sendSize := size
	var sendContent io.Reader = content
	if size >= 0 {
		ranges, err := parseRange(rangeReq, size)
		if err != nil {
			if err == errNoOverlap {
				w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", size))
			}
			// TODO error
			return
		}
		if sumRangesSize(ranges) > size {
			// The total number of bytes in all the ranges
			// is larger than the size of the file by
			// itself, so this is probably an attack, or a
			// dumb client. Ignore the range request.
			ranges = nil
		}
		switch {
		case len(ranges) == 1:
			ra := ranges[0]
			if _, err := content.Seek(ra.start, io.SeekStart); err != nil {
				// TODO error
				return
			}
			sendSize = ra.length
			code = http.StatusPartialContent
			w.Header().Set("Content-Range", contentRange(ra, size))
		case len(ranges) > 1:
			sendSize = rangesMIMESize(ranges, ctype, size)
			code = http.StatusPartialContent

			pr, pw := io.Pipe()
			mw := multipart.NewWriter(pw)
			w.Header().Set("Content-Type", "multipart/byteranges; boundary="+mw.Boundary())
			sendContent = pr
			defer pr.Close() // cause writing goroutine to fail and exit if CopyN doesn't finish.
			go func() {
				for _, ra := range ranges {
					part, err := mw.CreatePart(mimeHeader(ra, ctype, size))
					if err != nil {
						pw.CloseWithError(err)
						return
					}
					if _, err := content.Seek(ra.start, io.SeekStart); err != nil {
						pw.CloseWithError(err)
						return
					}
					if _, err := io.CopyN(part, content, ra.length); err != nil {
						pw.CloseWithError(err)
						return
					}
				}
				mw.Close()
				pw.Close()
			}()
		}

		w.Header().Set("Accept-Ranges", "bytes")
		if w.Header().Get("Content-Encoding") == "" {
			w.Header().Set("Content-Length", strconv.FormatInt(sendSize, 10))
		}
	}

	w.WriteHeader(code)

	if req.Method != "HEAD" {
		io.CopyN(w, sendContent, sendSize)
	}
}

func parseRange(s string, size int64) ([]httpRange, error) {
	if s == "" {
		return nil, nil // header not present
	}
	const b = "bytes="
	if !strings.HasPrefix(s, b) {
		return nil, errors.New("invalid range")
	}
	var ranges []httpRange
	noOverlap := false
	for _, ra := range strings.Split(s[len(b):], ",") {
		ra = strings.TrimSpace(ra)
		if ra == "" {
			continue
		}
		i := strings.Index(ra, "-")
		if i < 0 {
			return nil, errors.New("invalid range")
		}
		start, end := strings.TrimSpace(ra[:i]), strings.TrimSpace(ra[i+1:])
		var r httpRange
		if start == "" {
			// If no start is specified, end specifies the
			// range start relative to the end of the file.
			i, err := strconv.ParseInt(end, 10, 64)
			if err != nil {
				return nil, errors.New("invalid range")
			}
			if i > size {
				i = size
			}
			r.start = size - i
			r.length = size - r.start
		} else {
			i, err := strconv.ParseInt(start, 10, 64)
			if err != nil || i < 0 {
				return nil, errors.New("invalid range")
			}
			if i >= size {
				// If the range begins after the size of the content,
				// then it does not overlap.
				noOverlap = true
				continue
			}
			r.start = i
			if end == "" {
				// If no end is specified, range extends to end of the file.
				r.length = size - r.start
			} else {
				i, err := strconv.ParseInt(end, 10, 64)
				if err != nil || r.start > i {
					return nil, errors.New("invalid range")
				}
				if i >= size {
					i = size - 1
				}
				r.length = i - r.start + 1
			}
		}
		ranges = append(ranges, r)
	}
	if noOverlap && len(ranges) == 0 {
		// The specified ranges did not overlap with the content.
		return nil, errNoOverlap
	}
	return ranges, nil
}

func sumRangesSize(ranges []httpRange) (size int64) {
	for _, ra := range ranges {
		size += ra.length
	}
	return
}

func contentRange(r httpRange, size int64) string {
	return fmt.Sprintf("bytes %d-%d/%d", r.start, r.start+r.length-1, size)
}

func mimeHeader(r httpRange, contentType string, size int64) textproto.MIMEHeader {
	return textproto.MIMEHeader{
		"Content-Range": {contentRange(r, size)},
		"Content-Type":  {contentType},
	}
}

func rangesMIMESize(ranges []httpRange, contentType string, contentSize int64) (encSize int64) {
	var w countingWriter
	mw := multipart.NewWriter(&w)
	for _, ra := range ranges {
		mw.CreatePart(mimeHeader(ra, contentType, contentSize))
		encSize += ra.length
	}
	mw.Close()
	encSize += int64(w)
	return
}

func mkdirRec(dirname string) {
	_, err := os.Stat(dirname)
	if err == nil {
		return
	}

	mkdirRec(filepath.Dir(dirname))
	os.Mkdir(dirname, 0777)
}

// https://medium.com/eureka-engineering/multipart-file-upload-in-golang-c4a8eb15a3ee
func handleUpload(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	// TODO handle multiple files with one request.

	name := req.FormValue("name")
	formFile, _, err := req.FormFile("data")
	//formFile, fileHeader, err := req.FormFile("data")
	if err != nil {
		// TODO error
		fmt.Println("error")
		return
	}
	defer formFile.Close()

	topPath := "./"
	filename, _ := filepath.Abs(topPath + name)

	fmt.Println(filename)
	_, err = os.Stat(filepath.Dir(filename))
	if err != nil {
		mkdirRec(filepath.Dir(filename))
	}

	saveFile, err := os.Create(filename)
	if err != nil {
		// TODO error
		fmt.Println("error")
		return
	}
	defer saveFile.Close()

	_, err = io.Copy(saveFile, formFile)
	if err != nil {
		// TODO error
		fmt.Println("error")
		return
	}

	w.WriteHeader(http.StatusCreated)
}

func main() {
	http.HandleFunc("/", router)
	// TODO port
	// TODO target directory
	log.Fatal(http.ListenAndServe(":9999", nil))
}
