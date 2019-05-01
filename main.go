package main

import (
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
)

var root = http.Dir(".")

func isDirPath(s string) bool {
	return s[len(s)-1] == '/'
}

func redirect(w http.ResponseWriter, req *http.Request, newPath string) {
	w.Header().Set("Location", newPath)
	w.WriteHeader(http.StatusMovedPermanently)
}

func fileHandler(w http.ResponseWriter, req *http.Request) {

	//targetPath := path.Clean(req.URL.Path)
	targetPath := req.URL.Path
	fmt.Println(targetPath)

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

		html := "<html><head></head><body><ul>"
		for _, f := range files {
			name := f.Name()
			href := fmt.Sprintf("%s%s", req.RequestURI, name)
			html += fmt.Sprintf(`<li><a href="%s">%s</a></li>`, href, name)
		}
		html += "<ul><p>hello</p></body></html>"
		io.WriteString(w, html)
	} else {
		io.WriteString(w, "serve file")
	}
}

func main() {
	http.HandleFunc("/", fileHandler)
	log.Fatal(http.ListenAndServe(":8080", nil))
}
