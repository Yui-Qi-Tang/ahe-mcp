package newsfeed

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

func parseRSS(raw []byte, limit int) ([]Item, error) {
	bad := errors.New("news source must be bounded UTF-8 RSS 2.0 without DTD or custom entities")
	if len(raw) == 0 || len(raw) > maxXMLBytes || !utf8.Valid(raw) {
		return nil, bad
	}
	// A UTF-8 BOM is encoding metadata, not item text; RawXML still retains it.
	d := xml.NewDecoder(bytes.NewReader(bytes.TrimPrefix(raw, []byte{0xef, 0xbb, 0xbf})))
	var stack []xml.Name
	var current map[string]string
	var field string
	var items = []Item{}
	rootSeen, channelSeen, declarationSeen := false, false, false
	for {
		token, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, bad
		}
		switch value := token.(type) {
		case xml.Directive:
			return nil, bad
		case xml.ProcInst:
			if rootSeen || declarationSeen || value.Target != "xml" {
				return nil, bad
			}
			declarationSeen = true
		case xml.StartElement:
			if len(stack) >= 32 || field != "" {
				return nil, bad
			}
			seenAttrs := make(map[xml.Name]bool)
			for _, attr := range value.Attr {
				if seenAttrs[attr.Name] {
					return nil, bad
				}
				seenAttrs[attr.Name] = true
			}
			if len(stack) == 0 {
				if rootSeen || value.Name != (xml.Name{Local: "rss"}) {
					return nil, bad
				}
				version := ""
				for _, attr := range value.Attr {
					if attr.Name == (xml.Name{Local: "version"}) {
						version = attr.Value
					}
				}
				if version != "2.0" {
					return nil, bad
				}
				rootSeen = true
			} else if len(stack) == 1 {
				if channelSeen || value.Name != (xml.Name{Local: "channel"}) {
					return nil, bad
				}
				channelSeen = true
			} else if len(stack) == 2 && value.Name == (xml.Name{Local: "item"}) {
				current = make(map[string]string)
			} else if len(stack) == 3 && current != nil && value.Name.Space == "" {
				switch value.Name.Local {
				case "title", "link", "description", "pubDate":
					if _, exists := current[value.Name.Local]; exists {
						return nil, bad
					}
					field = value.Name.Local
					current[field] = ""
				}
			}
			stack = append(stack, value.Name)
		case xml.CharData:
			if field != "" {
				current[field] += string(value)
			} else if len(stack) == 0 && strings.TrimSpace(string(value)) != "" {
				return nil, bad
			}
		case xml.EndElement:
			if len(stack) == 0 {
				return nil, bad
			}
			if len(stack) == 4 && field != "" {
				field = ""
			}
			if len(stack) == 3 && value.Name == (xml.Name{Local: "item"}) && current != nil {
				if strings.TrimSpace(current["title"]) == "" || !articleURL(current["link"]) {
					return nil, bad
				}
				if len(items) < limit {
					items = append(items, Item{Title: current["title"], URL: current["link"],
						Description: current["description"], PublishedAt: current["pubDate"]})
				}
				current = nil
			}
			stack = stack[:len(stack)-1]
		}
	}
	if !rootSeen || !channelSeen || len(stack) != 0 {
		return nil, bad
	}
	return items, nil
}

func articleURL(raw string) bool {
	for _, r := range raw {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return false
		}
	}
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Hostname() != "" && u.User == nil && u.Opaque == ""
}
