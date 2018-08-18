package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"strings"
	"time"

	"perkeep.org/pkg/blob"
)

type TextFilter func(*Tiddler) bool

func AlwaysIncludeText(_ *Tiddler) bool { return true }
func NeverIncludeText(_ *Tiddler) bool  { return false }

type TiddlyServer interface {
	GenerateIndex(ctx context.Context, w io.Writer) error

	PutTiddler(context.Context, *Tiddler) error
	GetTiddler(context.Context, TiddlerRef, TextFilter) (*Tiddler, error)
	DeleteTiddler(context.Context, TiddlerRef) error

	ListBag(context.Context, string, TextFilter) ([]*Tiddler, error)
	ListRecipe(context.Context, string, TextFilter) ([]*Tiddler, error)
}

type TiddlerMeta struct {
	TiddlerRef

	Fields      map[string]interface{} `json:"fields,omitempty"`
	Type        string                 `json:"type,omitempty"`
	Permissions string                 `json:"permissions,omitempty"`

	Created  string `json:"created,omitempty"`
	Creator  string `json:"creator,omitempty"`
	Modified string `json:"modified,omitempty"`
	Modifier string `json:"modifier,omitempty"`
}

func (t *TiddlerMeta) MergeFrom(tiddlerMeta map[string]interface{}) {
	if fields, ok := tiddlerMeta["fields"].(map[string]interface{}); ok {
		t.Fields = fields
	} else {
		t.Fields = make(map[string]interface{})
	}

	for f, v := range tiddlerMeta {
		switch f {
		case "title":
			t.Title = toString(v)
		case "revision":
			if rev, err := time.Parse(time.RFC3339, toString(v)); err == nil {
				t.Revision = rev
			}
		case "tags":
			switch tags := v.(type) {
			case []string:
				t.Tags = tags
			case []interface{}:
				t.Tags = nil
				for _, tag := range tags {
					t.Tags = append(t.Tags, toString(tag))
				}
			default:
				log.Printf("WARNING: unknown tags type: %T", tags)
			}
		case "type":
			t.Type = toString(v)
		case "bag":
			t.Bag = toString(v)
		case "recipe":
			t.Recipe = toString(v)
		case "permissions":
			t.Permissions = toString(v)
		case "creator":
			t.Creator = toString(v)
		case "created":
			t.Created = toString(v)
		case "modifier":
			t.Modifier = toString(v)
		case "modified":
			t.Modified = toString(v)
		case "text":
			// skip
		case "fields":
			// handled above
		default:
			t.Fields[f] = v
		}
	}
}

type Tiddler struct {
	TiddlerMeta

	Text string `json:"text,omitempty"`
}

func addIfNonEmpty(obj map[string]interface{}, field, val string) {
	if val != "" {
		obj[field] = val
	}
}

func (t *Tiddler) ToJSONObject() (map[string]interface{}, error) {
	obj := make(map[string]interface{})
	for f, v := range t.Fields {
		obj[f] = v
	}
	obj["title"] = t.Title

	rev, err := t.Revision.MarshalText()
	if err != nil {
		return nil, err
	}
	obj["revision"] = string(rev)

	addIfNonEmpty(obj, "tiddlykeep-ref", t.Ref.String())
	addIfNonEmpty(obj, "bag", t.Bag)
	addIfNonEmpty(obj, "recipe", t.Recipe)
	addIfNonEmpty(obj, "type", t.Type)
	addIfNonEmpty(obj, "text", t.Text)
	addIfNonEmpty(obj, "permissions", t.Permissions)
	addIfNonEmpty(obj, "created", t.Created)
	addIfNonEmpty(obj, "creator", t.Creator)
	addIfNonEmpty(obj, "modified", t.Modified)
	addIfNonEmpty(obj, "modifier", t.Modifier)

	if len(t.Tags) != 0 {
		obj["tags"] = t.Tags
	}
	return obj, nil
}

func (t *Tiddler) MarshalJSON() ([]byte, error) {
	obj, err := t.ToJSONObject()
	if err != nil {
		return nil, err
	}
	return json.Marshal(obj)
}

func (t *Tiddler) MarshalText() ([]byte, error) {
	obj, err := t.ToJSONObject()
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	for f, val := range obj {
		if f == "text" {
			continue
		}
		switch val := val.(type) {
		case []string:
			var ss []string
			for _, s := range val {
				if strings.Contains(s, " ") {
					ss = append(ss, "[["+s+"]]")
				} else {
					ss = append(ss, s)
				}
			}
			fmt.Fprintf(&b, "%s: %s\n", f, strings.Join(ss, " "))
		default:
			fmt.Fprintf(&b, "%s: %v\n", f, val)
		}
	}
	b.WriteRune('\n')
	text, _ := obj["text"].(string)
	b.WriteString(text)
	return b.Bytes(), nil
}

func (t *Tiddler) UnmarshalText(data []byte) error {
	obj := make(map[string]interface{})
	s := bufio.NewReader(bytes.NewReader(data))
	for {
		b, isPrefix, err := s.ReadLine()
		if err != nil {
			return err
		} else if isPrefix {
			return fmt.Errorf("line too big: TODO")
		} else if len(b) == 0 {
			break
		}
		line := string(b)
		p := strings.SplitN(line, ": ", 2)
		if len(p) != 2 {
			return fmt.Errorf("malformatted field: %s", line)
		}
		if p[0] == "tags" {
			split := strings.Split(p[1], " ")
			var tags []string
			var tag string
			for _, t := range split {
				trimmed := strings.TrimSpace(t)
				if tag == "" && !strings.HasPrefix(trimmed, "[[") {
					tags = append(tags, trimmed)
				} else {
					tag += " " + t
					if strings.HasSuffix(trimmed, "]]") {
						tags = append(tags, strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(tag), "[["), "]]"))
						tag = ""
					}
				}
			}
			if tag != "" {
				tags = append(tags, strings.TrimPrefix(strings.TrimSpace(tag), "[["))
			}
			obj["tags"] = tags
		} else {
			obj[p[0]] = p[1]
		}
	}
	var text bytes.Buffer
	if _, err := s.WriteTo(&text); err != nil {
		return err
	}
	obj["text"] = text.String()
	t.MergeFrom(obj)
	return nil
}

func (t *Tiddler) UnmarshalJSON(data []byte) error {
	var tiddlerMeta map[string]interface{}
	if err := json.Unmarshal(data, &tiddlerMeta); err != nil {
		return err
	}
	t.MergeFrom(tiddlerMeta)
	return nil
}

func toString(v interface{}) string {
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%s", v)
}

func (t *Tiddler) MergeFrom(tiddlerMeta map[string]interface{}) {
	t.TiddlerMeta.MergeFrom(tiddlerMeta)
	if text, ok := tiddlerMeta["text"]; ok {
		t.Text = toString(text)
	}
}

type TiddlerRef struct {
	Ref     blob.Ref `json:"-"`
	TextRef blob.Ref `json:"-"`
	MetaRef blob.Ref `json:"-"`

	Title  string   `json:"title"`
	Bag    string   `json:"bag,omitempty"`
	Recipe string   `json:"recipe,omitempty"`
	Tags   []string `json:"tags,omitempty"`

	Revision time.Time `json:"revision"`
}
