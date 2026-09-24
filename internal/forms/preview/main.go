// Command preview writes sample form mails as .eml and .html, so their look
// can be checked in real mail clients before a change ships:
//
//	go run ./internal/forms/preview [-out DIR]
package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ittrail/sitebin.io/internal/forms"
)

func main() {
	out := flag.String("out", filepath.Join(os.TempDir(), "sitebin-mail-preview"), "directory to write the samples to")
	flag.Parse()
	if err := os.MkdirAll(*out, 0o755); err != nil {
		log.Fatal(err)
	}
	sub := &forms.Submission{
		Fields: []forms.Field{
			{Name: "name", Value: "Anna Muster"},
			{Name: "email", Value: "anna@example.com"},
			{Name: "phone", Value: "+43 660 1234567"},
			{Name: "topic", Value: "Hosting"},
			{Name: "topic", Value: "Domains"},
			{Name: "message", Value: "Hallo!\n\nIch hätte gerne ein Angebot für eine Website mit drei Unterseiten.\nGrüße aus Linz 👋"},
			{Name: "newsletter", Value: ""},
		},
		Control: map[string]string{},
		Files:   []forms.File{{Field: "attachments", Filename: "Skizze Startseite.pdf", ContentType: "application/pdf", Data: make([]byte, 183244)}},
	}
	sm, err := forms.BuildSubmission(forms.SubmissionMail{
		From: "forms@sitebin.io", FormName: "Kontakt", FormKey: "k7f3m2q9xaw4npd6", Recipient: "office@example.com",
		SiteID: "abcdefghijklmnopqrstuvwxyz", Host: "www.example.com",
		StopURL: "https://app.sitebin.io/forms/stop?t=preview", At: time.Now(), Sub: sub,
	})
	if err != nil {
		log.Fatal(err)
	}
	cm, err := forms.BuildConfirmation(forms.ConfirmationMail{
		From: "forms@sitebin.io", FormName: "Kontakt", Recipient: "office@example.com",
		Host: "www.example.com", ConfirmURL: "https://app.sitebin.io/forms/confirm?t=preview", At: time.Now(),
	})
	if err != nil {
		log.Fatal(err)
	}
	write(*out, "submission", sm)
	write(*out, "confirmation", cm)
}

func write(dir, name string, m forms.Mail) {
	eml := filepath.Join(dir, name+".eml")
	if err := os.WriteFile(eml, m.Data, 0o644); err != nil {
		log.Fatal(err)
	}
	html := filepath.Join(dir, name+".html")
	if err := os.WriteFile(html, htmlPart(m.Data), 0o644); err != nil {
		log.Fatal(err)
	}
	fmt.Println("wrote", eml, "and", html)
}

// htmlPart digs the decoded text/html part out of a message.
func htmlPart(data []byte) []byte {
	msg, err := mail.ReadMessage(bytes.NewReader(data))
	if err != nil {
		log.Fatal(err)
	}
	var find func(ct string, r io.Reader) []byte
	find = func(ct string, r io.Reader) []byte {
		_, params, _ := mime.ParseMediaType(ct)
		mr := multipart.NewReader(r, params["boundary"])
		for {
			p, err := mr.NextRawPart()
			if err != nil {
				return nil
			}
			pct := p.Header.Get("Content-Type")
			if strings.HasPrefix(pct, "multipart/") {
				if b := find(pct, p); b != nil {
					return b
				}
				continue
			}
			if strings.HasPrefix(pct, "text/html") {
				b, _ := io.ReadAll(quotedprintable.NewReader(p))
				return b
			}
		}
	}
	return find(msg.Header.Get("Content-Type"), msg.Body)
}
