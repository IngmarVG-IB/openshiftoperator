/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package uddi

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// This file implements the subset of the Universal DDI _filter and _tfilter grammar the
// operator's adapters actually emit, so the in-memory fake can answer the same lookups
// as the real API:
//
//	expr    := orExpr
//	orExpr  := andExpr ( 'or' andExpr )*
//	andExpr := unary ( 'and' unary )*
//	unary   := 'not' unary | '(' expr ')' | compare
//	compare := ident op literal
//	ident   := bare identifier or a quoted string (tag keys are quoted)
//	op      := '==' | '!=' | '~' | '!~'
//	literal := quoted string | number | 'null'
//
// Regex operators are approximated with a case-sensitive substring match, which is
// enough for the fake. Anything outside the grammar is a parse error, which surfaces as
// an APIError with status 400 exactly as the API would reject it.

type filterExpr interface {
	eval(fields map[string]any) bool
}

type filterAnd struct{ l, r filterExpr }

func (e filterAnd) eval(f map[string]any) bool { return e.l.eval(f) && e.r.eval(f) }

type filterOr struct{ l, r filterExpr }

func (e filterOr) eval(f map[string]any) bool { return e.l.eval(f) || e.r.eval(f) }

type filterNot struct{ x filterExpr }

func (e filterNot) eval(f map[string]any) bool { return !e.x.eval(f) }

type filterCompare struct {
	field string
	op    string
	value any
}

func (e filterCompare) eval(f map[string]any) bool {
	got, present := f[e.field]
	if !present {
		got = nil
	}
	switch e.op {
	case "==":
		return valueEqual(got, e.value)
	case "!=":
		return !valueEqual(got, e.value)
	case "~":
		return strings.Contains(toString(got), toString(e.value))
	case "!~":
		return !strings.Contains(toString(got), toString(e.value))
	}
	return false
}

func valueEqual(got, want any) bool {
	if want == nil {
		return got == nil
	}
	if got == nil {
		return false
	}
	if gn, ok := toNumber(got); ok {
		if wn, ok2 := toNumber(want); ok2 {
			return gn == wn
		}
	}
	return toString(got) == toString(want)
}

func toNumber(v any) (float64, bool) {
	switch n := v.(type) {
	case int:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case float64:
		return n, true
	}
	return 0, false
}

func toString(v any) string {
	switch s := v.(type) {
	case nil:
		return ""
	case string:
		return s
	case int64:
		return strconv.FormatInt(s, 10)
	case int32:
		return strconv.FormatInt(int64(s), 10)
	case int:
		return strconv.Itoa(s)
	case float64:
		return strconv.FormatFloat(s, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(s)
	}
	return fmt.Sprint(v)
}

// parseFilter compiles a _filter or _tfilter expression. An empty string compiles to a
// predicate that matches everything.
func parseFilter(s string) (filterExpr, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	p := &filterParser{in: []rune(s)}
	e, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	p.skipSpace()
	if p.pos != len(p.in) {
		return nil, fmt.Errorf("unexpected trailing input at offset %d", p.pos)
	}
	return e, nil
}

// matchFilter reports whether fields satisfies the compiled expression.
func matchFilter(e filterExpr, fields map[string]any) bool {
	if e == nil {
		return true
	}
	return e.eval(fields)
}

type filterParser struct {
	in  []rune
	pos int
}

func (p *filterParser) skipSpace() {
	for p.pos < len(p.in) && unicode.IsSpace(p.in[p.pos]) {
		p.pos++
	}
}

func (p *filterParser) peekWord() string {
	p.skipSpace()
	start := p.pos
	for start < len(p.in) && (unicode.IsLetter(p.in[start]) || p.in[start] == '_') {
		start++
	}
	return string(p.in[p.pos:start])
}

func (p *filterParser) consumeWord(w string) bool {
	if !strings.EqualFold(p.peekWord(), w) {
		return false
	}
	p.skipSpace()
	p.pos += len(w)
	return true
}

func (p *filterParser) parseOr() (filterExpr, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for {
		save := p.pos
		if !p.consumeWord("or") {
			p.pos = save
			return left, nil
		}
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		left = filterOr{l: left, r: right}
	}
}

func (p *filterParser) parseAnd() (filterExpr, error) {
	left, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	for {
		save := p.pos
		if !p.consumeWord("and") {
			p.pos = save
			return left, nil
		}
		right, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		left = filterAnd{l: left, r: right}
	}
}

func (p *filterParser) parseUnary() (filterExpr, error) {
	p.skipSpace()
	if p.pos >= len(p.in) {
		return nil, fmt.Errorf("unexpected end of filter")
	}
	save := p.pos
	if p.consumeWord("not") {
		x, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		return filterNot{x: x}, nil
	}
	p.pos = save
	if p.in[p.pos] == '(' {
		p.pos++
		e, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		p.skipSpace()
		if p.pos >= len(p.in) || p.in[p.pos] != ')' {
			return nil, fmt.Errorf("missing closing parenthesis at offset %d", p.pos)
		}
		p.pos++
		return e, nil
	}
	return p.parseCompare()
}

func (p *filterParser) parseCompare() (filterExpr, error) {
	field, err := p.parseIdent()
	if err != nil {
		return nil, err
	}
	op, err := p.parseOp()
	if err != nil {
		return nil, err
	}
	value, err := p.parseLiteral()
	if err != nil {
		return nil, err
	}
	return filterCompare{field: field, op: op, value: value}, nil
}

func (p *filterParser) parseIdent() (string, error) {
	p.skipSpace()
	if p.pos >= len(p.in) {
		return "", fmt.Errorf("expected a field name")
	}
	if q := p.in[p.pos]; q == '"' || q == '\'' {
		return p.parseQuoted()
	}
	start := p.pos
	for p.pos < len(p.in) {
		c := p.in[p.pos]
		if unicode.IsLetter(c) || unicode.IsDigit(c) || c == '_' || c == '.' || c == '-' {
			p.pos++
			continue
		}
		break
	}
	if p.pos == start {
		return "", fmt.Errorf("expected a field name at offset %d", p.pos)
	}
	return string(p.in[start:p.pos]), nil
}

func (p *filterParser) parseQuoted() (string, error) {
	q := p.in[p.pos]
	p.pos++
	var b strings.Builder
	for p.pos < len(p.in) {
		c := p.in[p.pos]
		if c == '\\' && p.pos+1 < len(p.in) {
			p.pos++
			b.WriteRune(p.in[p.pos])
			p.pos++
			continue
		}
		if c == q {
			p.pos++
			return b.String(), nil
		}
		b.WriteRune(c)
		p.pos++
	}
	return "", fmt.Errorf("unterminated string literal")
}

func (p *filterParser) parseOp() (string, error) {
	p.skipSpace()
	rest := string(p.in[p.pos:])
	for _, op := range []string{"==", "!=", "!~", "~"} {
		if strings.HasPrefix(rest, op) {
			p.pos += len(op)
			return op, nil
		}
	}
	return "", fmt.Errorf("expected a comparison operator at offset %d", p.pos)
}

func (p *filterParser) parseLiteral() (any, error) {
	p.skipSpace()
	if p.pos >= len(p.in) {
		return nil, fmt.Errorf("expected a literal")
	}
	if q := p.in[p.pos]; q == '"' || q == '\'' {
		return p.parseQuoted()
	}
	start := p.pos
	for p.pos < len(p.in) {
		c := p.in[p.pos]
		if unicode.IsLetter(c) || unicode.IsDigit(c) || c == '_' || c == '.' || c == '-' || c == '+' {
			p.pos++
			continue
		}
		break
	}
	tok := string(p.in[start:p.pos])
	switch {
	case tok == "":
		return nil, fmt.Errorf("expected a literal at offset %d", p.pos)
	case strings.EqualFold(tok, "null"):
		return nil, nil
	case strings.EqualFold(tok, "true"):
		return true, nil
	case strings.EqualFold(tok, "false"):
		return false, nil
	}
	if n, err := strconv.ParseFloat(tok, 64); err == nil {
		return n, nil
	}
	return tok, nil
}
