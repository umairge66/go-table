package db

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// SchemaReport is what a sample of a collection's documents says about its
// fields, as Compass's Schema tab shows it.
type SchemaReport struct {
	Sampled int64         `json:"sampled"`
	Total   int64         `json:"total"` // estimated
	Fields  []FieldReport `json:"fields"`
}

// FieldReport describes one field path: a.b for a nested one, a[] for the
// elements of an array.
type FieldReport struct {
	Path string `json:"path"`
	// Count: sampled documents (or, under an array, elements) that have it.
	Count int64       `json:"count"`
	Types []TypeCount `json:"types"`
	// Min and Max: of numbers and dates, as text.
	Min string `json:"min,omitempty"`
	Max string `json:"max,omitempty"`
	// Top: the most frequent values, when values repeat (strings, numbers,
	// booleans); Distinct counts them up to a cap, Unique says every value
	// differed.
	Top      []ValueCount `json:"top,omitempty"`
	Distinct int          `json:"distinct"`
	Unique   bool         `json:"unique"`
	// Of: how many values the counts are out of (documents, or elements).
	Of int64 `json:"of"`
}

type TypeCount struct {
	Type  string `json:"type"`
	Count int64  `json:"count"`
}

type ValueCount struct {
	Value string `json:"value"`
	Count int64  `json:"count"`
}

const schemaDistinctCap = 1000

type fieldAcc struct {
	path     string
	count    int64
	of       int64
	types    map[string]int64
	min, max any
	values   map[string]int64
	capped   bool
	order    int
}

// AnalyzeSchema samples up to n documents at random and describes their
// fields.
func (c *Conn) AnalyzeSchema(ctx context.Context, t TableRef, n int) (*SchemaReport, error) {
	if c.mongo == nil {
		return nil, errors.New("schema analysis is for MongoDB collections")
	}
	if n <= 0 {
		n = 1000
	}
	if err := c.lock(ctx); err != nil {
		return nil, err
	}
	defer c.mu.Unlock()
	coll := c.mongoColl(t)
	rep := &SchemaReport{Fields: []FieldReport{}}
	if total, err := coll.EstimatedDocumentCount(ctx); err == nil {
		rep.Total = total
	}
	// $sample reads the whole collection when asked for most of it; a plain
	// read is the same then, and works on views too.
	var pipeline bson.A
	if rep.Total == 0 || int64(n) < rep.Total {
		pipeline = bson.A{bson.D{{Key: "$sample", Value: bson.D{{Key: "size", Value: n}}}}}
	} else {
		pipeline = bson.A{bson.D{{Key: "$limit", Value: n}}}
	}
	cur, err := coll.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	acc := map[string]*fieldAcc{}
	get := func(path string) *fieldAcc {
		a := acc[path]
		if a == nil {
			a = &fieldAcc{path: path, types: map[string]int64{}, values: map[string]int64{}, order: len(acc)}
			acc[path] = a
		}
		return a
	}
	var walk func(prefix string, d bson.D)
	var value func(path string, v any)
	value = func(path string, v any) {
		a := get(path)
		a.count++
		ty := bsonType(v)
		a.types[ty]++
		switch x := v.(type) {
		case bson.D:
			walk(path+".", x)
		case bson.A:
			ea := get(path + "[]")
			ea.of += int64(len(x))
			for _, e := range x {
				value(path+"[]", e)
			}
		}
		if k, ok := rangeKey(v); ok {
			if a.min == nil || less(k, a.min) {
				a.min = k
			}
			if a.max == nil || less(a.max, k) {
				a.max = k
			}
		}
		if s, ok := topKey(v); ok && !a.capped {
			a.values[s]++
			if len(a.values) > schemaDistinctCap {
				a.capped = true
				a.values = nil
			}
		}
	}
	walk = func(prefix string, d bson.D) {
		for _, e := range d {
			value(prefix+e.Key, e.Value)
		}
	}
	for cur.Next(ctx) {
		var d bson.D
		if err := cur.Decode(&d); err != nil {
			return nil, err
		}
		rep.Sampled++
		walk("", d)
	}
	if err := cur.Err(); err != nil {
		return nil, err
	}
	// Every top-level path is out of the sampled documents; a nested one is
	// out of the documents (or elements) its parent was a document in.
	list := make([]*fieldAcc, 0, len(acc))
	for _, a := range acc {
		list = append(list, a)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].order < list[j].order })
	for _, a := range list {
		if strings.HasSuffix(a.path, "[]") {
			// of was counted from the array lengths
		} else if parent, ok := parentPath(a.path); ok {
			if p := acc[parent]; p != nil {
				a.of = p.types["object"]
			}
		} else {
			a.of = rep.Sampled
		}
	}
	// Paths in document order, children after their parent.
	slices.SortStableFunc(list, func(x, y *fieldAcc) int { return strings.Compare(sortKey(x, acc), sortKey(y, acc)) })
	for _, a := range list {
		if a.count == 0 { // the elements of arrays that were all empty
			continue
		}
		f := FieldReport{Path: a.path, Count: a.count, Of: a.of, Types: []TypeCount{}}
		for ty, n := range a.types {
			f.Types = append(f.Types, TypeCount{ty, n})
		}
		sort.Slice(f.Types, func(i, j int) bool {
			if f.Types[i].Count != f.Types[j].Count {
				return f.Types[i].Count > f.Types[j].Count
			}
			return f.Types[i].Type < f.Types[j].Type
		})
		if a.min != nil {
			f.Min, f.Max = rangeText(a.min), rangeText(a.max)
		}
		if a.capped {
			f.Distinct = schemaDistinctCap
		} else {
			f.Distinct = len(a.values)
			var seen int64
			for _, n := range a.values {
				seen += n
			}
			f.Unique = seen > 1 && int64(len(a.values)) == seen
			// Values that repeat are worth listing; all different are not.
			if !f.Unique && len(a.values) > 0 {
				for v, n := range a.values {
					f.Top = append(f.Top, ValueCount{v, n})
				}
				sort.Slice(f.Top, func(i, j int) bool {
					if f.Top[i].Count != f.Top[j].Count {
						return f.Top[i].Count > f.Top[j].Count
					}
					return f.Top[i].Value < f.Top[j].Value
				})
				f.Top = f.Top[:min(len(f.Top), 10)]
			}
		}
		rep.Fields = append(rep.Fields, f)
	}
	return rep, nil
}

// sortKey orders a path after its parent and in first-seen order among its
// siblings: each segment's first-seen position, zero-padded.
func sortKey(a *fieldAcc, acc map[string]*fieldAcc) string {
	var parts []string
	for p := a.path; ; {
		parts = append(parts, fmt.Sprintf("%06d", acc[p].order))
		parent, ok := parentPath(p)
		if !ok {
			break
		}
		p = parent
	}
	slices.Reverse(parts)
	return strings.Join(parts, "/")
}

// parentPath: a.b → a; a[] → a; a[].b → a[].
func parentPath(p string) (string, bool) {
	if strings.HasSuffix(p, "[]") {
		return strings.TrimSuffix(p, "[]"), true
	}
	if i := strings.LastIndexByte(p, '.'); i >= 0 {
		return p[:i], true
	}
	return "", false
}

// rangeKey: numbers as float64, dates as time.Time.
func rangeKey(v any) (any, bool) {
	switch x := v.(type) {
	case int32:
		return float64(x), true
	case int64:
		return float64(x), true
	case float64:
		return x, true
	case bson.Decimal128:
		var f float64
		if _, err := fmt.Sscan(x.String(), &f); err == nil {
			return f, true
		}
	case bson.DateTime:
		return x.Time().UTC(), true
	}
	return nil, false
}

func less(a, b any) bool {
	switch x := a.(type) {
	case float64:
		y, ok := b.(float64)
		return ok && x < y
	case time.Time:
		y, ok := b.(time.Time)
		return ok && x.Before(y)
	}
	return false
}

func rangeText(v any) string {
	switch x := v.(type) {
	case float64:
		return fmt.Sprint(x)
	case time.Time:
		return x.Format(time.RFC3339)
	}
	return ""
}

// topKey: the values whose frequency is worth counting.
func topKey(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		if len(x) > 200 {
			return "", false
		}
		return x, true
	case bool:
		return fmt.Sprint(x), true
	case int32, int64, float64:
		return fmt.Sprint(x), true
	case nil:
		return "null", true
	}
	return "", false
}
