package main

import (
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
)

func fileHandler(w http.ResponseWriter, req *http.Request) {
	target := "." // TODO routing with a request

	files, _ := ioutil.ReadDir(target) // TODO error handling

	html := "<html><head></head><body><ul>"
	for _, f := range files {
		html += fmt.Sprintf("<li>%s</li>", f.Name())
	}
	html += "<ul></body></html>"
	io.WriteString(w, html)
}

func main() {
	http.HandleFunc("/", fileHandler)
	log.Fatal(http.ListenAndServe(":8080", nil))
}
