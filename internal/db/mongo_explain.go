package db

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// QueryPlan is what explain says about a query, in the shape the plan view
// draws: the stages as a tree, with the totals and the indexes they used.
type QueryPlan struct {
	Verbosity string `json:"verbosity"`
	// Root is the last stage to run; its children feed it.
	Root PlanStage `json:"root"`
	// Totals, when the plan ran (verbosity executionStats or more).
	NReturned    *int64   `json:"nReturned,omitempty"`
	DocsExamined *int64   `json:"docsExamined,omitempty"`
	KeysExamined *int64   `json:"keysExamined,omitempty"`
	Ms           *int64   `json:"ms,omitempty"`
	Indexes      []string `json:"indexes"`
	CollScan     bool     `json:"collScan"`
	InMemorySort bool     `json:"inMemorySort"`
	// RejectedPlans counts the other plans the optimizer tried.
	RejectedPlans int `json:"rejectedPlans"`
}

// PlanStage is one stage: IXSCAN, FETCH, COLLSCAN, SORT, $group, a shard…
type PlanStage struct {
	Stage        string      `json:"stage"`
	Index        string      `json:"index,omitempty"`
	KeyPattern   string      `json:"keyPattern,omitempty"`
	Direction    string      `json:"direction,omitempty"`
	Filter       string      `json:"filter,omitempty"`
	Shard        string      `json:"shard,omitempty"`
	NReturned    *int64      `json:"nReturned,omitempty"`
	DocsExamined *int64      `json:"docsExamined,omitempty"`
	KeysExamined *int64      `json:"keysExamined,omitempty"`
	Ms           *int64      `json:"ms,omitempty"`
	Children     []PlanStage `json:"children,omitempty"`
}

var explainVerbosities = []string{"queryPlanner", "executionStats", "allPlansExecution"}

// explainOf finds explain in a collection call chain, in either of the
// shell's forms: db.c.explain(v).find(…) or db.c.find(…).explain(v). It
// returns the calls without it.
func explainOf(calls []shellCall) (verbosity string, rest []shellCall, ok bool, err error) {
	at := -1
	for i, c := range calls {
		if c.name == "explain" {
			at = i
		}
	}
	if at < 0 {
		return "", calls, false, nil
	}
	verbosity = "executionStats" // what Compass shows
	switch v := first(calls[at].args).(type) {
	case nil:
	case bool:
		verbosity = map[bool]string{true: "allPlansExecution", false: "queryPlanner"}[v]
	case string:
		for _, x := range explainVerbosities {
			if strings.EqualFold(v, x) {
				verbosity = x
			}
		}
		if !strings.EqualFold(v, verbosity) {
			return "", nil, true, fmt.Errorf("explain takes %s", strings.Join(explainVerbosities, ", "))
		}
	default:
		return "", nil, true, errors.New(`explain takes a verbosity, e.g. "executionStats"`)
	}
	switch {
	case at == 0 && len(calls) > 1: // db.c.explain().find(…)
		rest = calls[1:]
	case at == len(calls)-1 && at > 0: // db.c.find(…).explain()
		rest = calls[:at]
	default:
		return "", nil, true, errors.New("write db.coll.find(…).explain() or db.coll.explain().find(…)")
	}
	return verbosity, rest, true, nil
}

// runExplain explains find, findOne, aggregate, count and distinct.
func runExplain(ctx context.Context, db *mongo.Database, coll string, calls []shellCall, verbosity string, r *Result) error {
	call, chain := calls[0], calls[1:]
	var cmd bson.D
	switch call.name {
	case "find", "findOne":
		f, err := parseFind(call.args, chain)
		if err != nil {
			return err
		}
		if call.name == "findOne" {
			f.limit = 1
		}
		if f.counting {
			cmd = bson.D{{Key: "count", Value: coll}, {Key: "query", Value: orEmpty(f.filter)}}
		} else {
			cmd = f.command(coll)
		}
	case "aggregate":
		pipeline, allowDiskUse, err := parseAggregate(call.args, chain)
		if err != nil {
			return err
		}
		cmd = bson.D{{Key: "aggregate", Value: coll}, {Key: "pipeline", Value: pipeline}, {Key: "cursor", Value: bson.D{}}}
		if allowDiskUse {
			cmd = append(cmd, bson.E{Key: "allowDiskUse", Value: true})
		}
	case "count", "countDocuments":
		filter, err := docArg(call.args, 0, "the filter")
		if err != nil {
			return err
		}
		cmd = bson.D{{Key: "count", Value: coll}, {Key: "query", Value: orEmpty(filter)}}
	case "distinct":
		field, ok := first(call.args).(string)
		if !ok {
			return errors.New("distinct takes a field name")
		}
		filter, err := docArg(call.args, 1, "the filter")
		if err != nil {
			return err
		}
		cmd = bson.D{{Key: "distinct", Value: coll}, {Key: "key", Value: field}, {Key: "query", Value: orEmpty(filter)}}
	default:
		return fmt.Errorf("%s() can't be explained here: explain find, aggregate, count or distinct", call.name)
	}
	if call.name != "find" && call.name != "aggregate" && len(chain) > 0 {
		return fmt.Errorf("%s() can't be followed by .%s()", call.name, chain[0].name)
	}
	var doc bson.D
	err := db.RunCommand(ctx, bson.D{{Key: "explain", Value: cmd}, {Key: "verbosity", Value: verbosity}}).Decode(&doc)
	if err != nil {
		return err
	}
	docRows(r, []bson.D{doc})
	r.Plan = summarizePlan(doc, verbosity)
	return nil
}

func orEmpty(d bson.D) bson.D {
	if d == nil {
		return bson.D{}
	}
	return d
}

// summarizePlan reads explain's output, whatever its shape: a classic plan,
// a slot-based one (7.0+, under queryPlan), an aggregation's stages, or a
// sharded cluster's per-shard plans.
func summarizePlan(doc bson.D, verbosity string) *QueryPlan {
	p := &QueryPlan{Verbosity: verbosity, Indexes: []string{}}
	p.Root = planOf(doc)
	// Totals: the executionStats sections, summed over shards.
	var stats []bson.D
	collectSection(doc, "executionStats", &stats)
	for _, st := range stats {
		addTo(&p.DocsExamined, num(st, "totalDocsExamined"))
		addTo(&p.KeysExamined, num(st, "totalKeysExamined"))
		if ms := num(st, "executionTimeMillis"); ms != nil && (p.Ms == nil || *ms > *p.Ms) {
			p.Ms = ms
		}
	}
	if top, ok := mongoGet(doc, "executionStats"); ok {
		if d, ok := top.(bson.D); ok {
			p.NReturned = num(d, "nReturned")
		}
	} else if len(stats) > 0 {
		p.NReturned = p.Root.NReturned
	}
	var planners []bson.D
	collectSection(doc, "queryPlanner", &planners)
	for _, qp := range planners {
		if rp, ok := mongoGet(qp, "rejectedPlans"); ok {
			if a, ok := rp.(bson.A); ok {
				p.RejectedPlans += len(a)
			}
		}
	}
	seen := map[string]bool{}
	var walk func(s PlanStage)
	walk = func(s PlanStage) {
		switch {
		case s.Index != "" && !seen[s.Index]:
			seen[s.Index] = true
			p.Indexes = append(p.Indexes, s.Index)
		case s.Stage == "COLLSCAN":
			p.CollScan = true
		case s.Stage == "SORT" || s.Stage == "$sort":
			p.InMemorySort = true
		}
		for _, ch := range s.Children {
			walk(ch)
		}
	}
	walk(p.Root)
	return p
}

// planOf finds the plan in one explain document.
func planOf(doc bson.D) PlanStage {
	// An aggregation: its stages in order, the first holding a find plan.
	if v, ok := mongoGet(doc, "stages"); ok {
		if a, ok := v.(bson.A); ok {
			return pipelineOf(a)
		}
	}
	// A sharded aggregation: each shard's own explain.
	if v, ok := mongoGet(doc, "shards"); ok {
		if d, ok := v.(bson.D); ok {
			root := PlanStage{Stage: "SHARD_MERGE"}
			if mt, ok := mongoGet(doc, "mergeType"); ok {
				root.Stage = "MERGE (" + fmt.Sprint(mt) + ")"
			}
			for _, e := range d {
				if sd, ok := e.Value.(bson.D); ok {
					ch := planOf(sd)
					root.Children = append(root.Children, PlanStage{Stage: "SHARD", Shard: e.Key, Children: []PlanStage{ch}})
				}
			}
			return root
		}
	}
	// Executed stages carry the counts; the slot-based engine's are in another
	// language, so its plan is read from queryPlan instead.
	if es := section(doc, "executionStats"); es != nil {
		if st := section(es, "executionStages"); st != nil && !isSlotBased(doc) {
			return stageOf(st)
		}
	}
	if qp := section(doc, "queryPlanner"); qp != nil {
		if wp := section(qp, "winningPlan"); wp != nil {
			if q := section(wp, "queryPlan"); q != nil {
				return stageOf(q)
			}
			return stageOf(wp)
		}
	}
	return PlanStage{Stage: "?"}
}

func isSlotBased(doc bson.D) bool {
	qp := section(doc, "queryPlanner")
	if qp == nil {
		return false
	}
	wp := section(qp, "winningPlan")
	return wp != nil && section(wp, "queryPlan") != nil
}

// pipelineOf turns an aggregation's stages into a chain, the last on top.
func pipelineOf(stages bson.A) PlanStage {
	var below *PlanStage
	for _, v := range stages {
		d, ok := v.(bson.D)
		if !ok || len(d) == 0 {
			continue
		}
		var s PlanStage
		if c, ok := d[0].Value.(bson.D); ok && d[0].Key == "$cursor" {
			s = planOf(c)
		} else {
			s = PlanStage{Stage: d[0].Key, Filter: compactJSON(d[0].Value)}
			s.NReturned = num(d, "nReturned")
			s.Ms = num(d, "executionTimeMillisEstimate")
		}
		if below != nil {
			s.Children = append(s.Children, *below)
		}
		below = &s
	}
	if below == nil {
		return PlanStage{Stage: "?"}
	}
	return *below
}

// stageOf reads one classic stage and the ones that feed it.
func stageOf(d bson.D) PlanStage {
	s := PlanStage{Stage: str(d, "stage")}
	s.Index = str(d, "indexName")
	if kp, ok := mongoGet(d, "keyPattern"); ok {
		s.KeyPattern = compactJSON(kp)
	}
	s.Direction = str(d, "direction")
	if f, ok := mongoGet(d, "filter"); ok {
		s.Filter = compactJSON(f)
	}
	s.NReturned = num(d, "nReturned")
	s.DocsExamined = num(d, "docsExamined")
	s.KeysExamined = num(d, "keysExamined")
	s.Ms = num(d, "executionTimeMillisEstimate")
	if s.Stage == "IDHACK" || strings.HasPrefix(s.Stage, "EXPRESS") { // _id lookups
		if s.Index == "" {
			s.Index = "_id_"
		}
	}
	if in := section(d, "inputStage"); in != nil {
		s.Children = append(s.Children, stageOf(in))
	}
	if v, ok := mongoGet(d, "inputStages"); ok {
		if a, ok := v.(bson.A); ok {
			for _, x := range a {
				if xd, ok := x.(bson.D); ok {
					s.Children = append(s.Children, stageOf(xd))
				}
			}
		}
	}
	// mongos: one plan per shard.
	if v, ok := mongoGet(d, "shards"); ok {
		if a, ok := v.(bson.A); ok {
			for _, x := range a {
				xd, ok := x.(bson.D)
				if !ok {
					continue
				}
				sh := PlanStage{Stage: "SHARD", Shard: str(xd, "shardName")}
				sh.NReturned = num(xd, "nReturned")
				sh.DocsExamined = num(xd, "totalDocsExamined")
				sh.KeysExamined = num(xd, "totalKeysExamined")
				for _, k := range []string{"executionStages", "winningPlan"} {
					if p := section(xd, k); p != nil {
						if q := section(p, "queryPlan"); q != nil {
							p = q
						}
						sh.Children = append(sh.Children, stageOf(p))
						break
					}
				}
				s.Children = append(s.Children, sh)
			}
		}
	}
	return s
}

func section(d bson.D, key string) bson.D {
	v, _ := mongoGet(d, key)
	x, _ := v.(bson.D)
	return x
}

// collectSection gathers every key section in a document, not looking
// inside the ones it found.
func collectSection(v any, key string, out *[]bson.D) {
	switch x := v.(type) {
	case bson.D:
		for _, e := range x {
			if d, ok := e.Value.(bson.D); ok && e.Key == key {
				*out = append(*out, d)
				continue
			}
			collectSection(e.Value, key, out)
		}
	case bson.A:
		for _, e := range x {
			collectSection(e, key, out)
		}
	}
}

func str(d bson.D, key string) string {
	v, _ := mongoGet(d, key)
	s, _ := v.(string)
	return s
}

func num(d bson.D, key string) *int64 {
	v, ok := mongoGet(d, key)
	if !ok {
		return nil
	}
	n, ok := mongoInt(v)
	if !ok {
		if f, isF := v.(float64); isF {
			n, ok = int64(f), true
		}
	}
	if !ok {
		return nil
	}
	return &n
}

func addTo(sum **int64, n *int64) {
	if n == nil {
		return
	}
	if *sum == nil {
		*sum = new(int64)
	}
	**sum += *n
}

func compactJSON(v any) string {
	d, ok := v.(bson.D)
	if !ok {
		d = bson.D{{Key: "v", Value: v}}
		s := mongoJSON(d, false)
		return strings.TrimSuffix(strings.TrimPrefix(s, "{ v: "), " }")
	}
	return mongoJSON(d, false)
}
