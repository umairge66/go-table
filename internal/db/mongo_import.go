package db

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// JSONImportOptions: how documents from a JSON file go into a collection.
type JSONImportOptions struct {
	// Truncate deletes every document first.
	Truncate bool
	// ContinueOnError skips documents the server refuses (a duplicate _id…)
	// and goes on, as Compass's "stop on errors" unchecked does.
	ContinueOnError bool
}

// JSONImportResult counts what an import did.
type JSONImportResult struct {
	Inserted int64
	// Failed: documents refused with ContinueOnError, and the first reasons.
	Failed int64
	Errors []ImportError
}

// ImportJSON reads documents into a MongoDB collection: a JSON array, as
// Compass exports, or one document after another (one a line, as
// mongoexport writes). Values are read as Extended JSON, so {"$oid": …},
// {"$date": …} and the like keep their types.
func (c *Conn) ImportJSON(ctx context.Context, r io.Reader, t TableRef, o JSONImportOptions, progress func(JSONImportResult)) (JSONImportResult, error) {
	var res JSONImportResult
	if c.mongo == nil {
		return res, errors.New("JSON documents import into MongoDB; for other databases import CSV or SQL")
	}
	br := bufio.NewReaderSize(r, 1<<20)
	// A byte order mark, then what the file holds.
	if b, _ := br.Peek(3); len(b) == 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF {
		br.Discard(3)
	}
	dec := json.NewDecoder(br)
	dec.UseNumber()
	array := false
	if first, err := firstNonSpace(br); err != nil {
		if errors.Is(err, io.EOF) {
			return res, errors.New("the file is empty")
		}
		return res, err
	} else if first == '[' {
		array = true
		if _, err := dec.Token(); err != nil {
			return res, err
		}
	} else if first != '{' {
		return res, fmt.Errorf("a JSON file of documents starts with [ or {, not %q", first)
	}

	if err := c.lock(ctx); err != nil {
		return res, err
	}
	defer c.mu.Unlock()
	coll := c.mongoColl(t)
	if o.Truncate {
		if _, err := coll.DeleteMany(ctx, bson.D{}); err != nil {
			return res, err
		}
	}
	var (
		batch []any
		first int // number of the batch's first document, from 1
		n     int
	)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		ins, failed, err := insertBatch(ctx, coll, batch, !o.ContinueOnError)
		res.Inserted += ins
		if err != nil && !(o.ContinueOnError && failed != nil) {
			return fmt.Errorf("documents %d–%d: %w (%d documents were imported)", first, first+len(batch)-1, err, res.Inserted)
		}
		for _, we := range failed {
			res.Failed++
			if len(res.Errors) < 10 {
				res.Errors = append(res.Errors, ImportError{Statement: fmt.Sprintf("document %d", first+we.Index), Message: we.Message})
			}
		}
		batch, first = batch[:0], n+1
		if progress != nil {
			progress(res)
		}
		return nil
	}
	first = 1
	for array && dec.More() || !array {
		var raw json.RawMessage
		err := dec.Decode(&raw)
		if errors.Is(err, io.EOF) && !array {
			break
		}
		n++
		if err != nil {
			return res, fmt.Errorf("document %d: %w", n, err)
		}
		var doc bson.D
		if err := bson.UnmarshalExtJSON(raw, false, &doc); err != nil {
			return res, fmt.Errorf("document %d isn't a document of Extended JSON: %w", n, err)
		}
		batch = append(batch, doc)
		if len(batch) >= 1000 {
			if err := flush(); err != nil {
				return res, err
			}
		}
	}
	if array {
		if _, err := dec.Token(); err != nil {
			return res, fmt.Errorf("after document %d: %w", n, err)
		}
	}
	if err := flush(); err != nil {
		return res, err
	}
	if n == 0 {
		return res, errors.New("the file has no documents")
	}
	return res, nil
}

func firstNonSpace(br *bufio.Reader) (byte, error) {
	for i := 1; ; i++ {
		b, err := br.Peek(i)
		if err != nil {
			return 0, err
		}
		switch c := b[i-1]; c {
		case ' ', '\t', '\r', '\n':
		default:
			return c, nil
		}
	}
}

// insertBatch inserts documents. Ordered stops at the first refused one;
// unordered goes on and returns the refused ones.
func insertBatch(ctx context.Context, coll *mongo.Collection, docs []any, ordered bool) (inserted int64, failed []mongo.BulkWriteError, err error) {
	_, err = coll.InsertMany(ctx, docs, options.InsertMany().SetOrdered(ordered))
	if err == nil {
		return int64(len(docs)), nil, nil
	}
	var bwe mongo.BulkWriteException
	if !errors.As(err, &bwe) || len(bwe.WriteErrors) == 0 || bwe.WriteConcernError != nil {
		return 0, nil, err
	}
	if ordered {
		return int64(bwe.WriteErrors[0].Index), nil, err
	}
	return int64(len(docs) - len(bwe.WriteErrors)), bwe.WriteErrors, err
}

// ExportJSON writes a collection's documents (those the page's filter and
// sort pick, all of them when its limit is 0) as a JSON array of Extended
// JSON, which ImportJSON and Compass read back. Relaxed reads like plain
// JSON; canonical keeps every type ({"$numberInt": "1"}). Columns, when
// given, keep only those fields.
func (c *Conn) ExportJSON(ctx context.Context, w io.Writer, t TableRef, p Page, columns []string, canonical bool, progress func()) error {
	if c.mongo == nil {
		return errors.New("documents export from MongoDB")
	}
	filter, err := mongoFilter(p, columns)
	if err != nil {
		return err
	}
	o := mongoFindOptions(p)
	if len(columns) > 0 {
		proj := bson.D{}
		for _, col := range columns {
			proj = append(proj, bson.E{Key: col, Value: 1})
		}
		if !slices.Contains(columns, "_id") {
			proj = append(proj, bson.E{Key: "_id", Value: 0})
		}
		o.SetProjection(proj)
	}
	if err := c.lock(ctx); err != nil {
		return err
	}
	defer c.mu.Unlock()
	cur, err := c.mongoColl(t).Find(ctx, filter, o)
	if err != nil {
		return err
	}
	defer cur.Close(ctx)
	bw := bufio.NewWriterSize(w, 1<<16)
	bw.WriteString("[")
	n := 0
	for cur.Next(ctx) {
		b, err := bson.MarshalExtJSON(cur.Current, canonical, false)
		if err != nil {
			return err
		}
		if n > 0 {
			bw.WriteString(",")
		}
		bw.WriteString("\n  ")
		bw.Write(b)
		n++
		if progress != nil {
			progress()
		}
	}
	if err := cur.Err(); err != nil {
		return err
	}
	if n > 0 {
		bw.WriteString("\n")
	}
	bw.WriteString("]\n")
	return bw.Flush()
}
