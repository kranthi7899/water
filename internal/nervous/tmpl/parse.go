package tmpl

import (
	"fmt"
	"strings"
)

// node is a compiled grammar AST node. Rule references are expanded (and
// checked for recursion) at compile time, so only these five shapes ever
// appear in a compiled Template.
type node interface{ isNode() }

// litNode matches exactly one literal token.
type litNode struct{ word string }

// seqNode matches its items in order.
type seqNode struct{ items []node }

// altNode matches exactly one of its branches: (a|b|c).
type altNode struct{ branches []node }

// optNode matches one of its branches, or nothing at all: [x] or [a|b].
type optNode struct{ branches []node }

// slotNode captures between min and max tokens for a named slot.
type slotNode struct {
	name     string
	min, max int
	isText   bool
}

func (litNode) isNode()  {}
func (seqNode) isNode()  {}
func (altNode) isNode()  {}
func (optNode) isNode()  {}
func (slotNode) isNode() {}

// tokKind identifies a lexical token in a template source string.
type tokKind int

const (
	tEOF tokKind = iota
	tLParen
	tRParen
	tLBracket
	tRBracket
	tLBrace
	tRBrace
	tLAngle
	tRAngle
	tPipe
	tWord
)

func (k tokKind) String() string {
	switch k {
	case tEOF:
		return "end of template"
	case tLParen:
		return "'('"
	case tRParen:
		return "')'"
	case tLBracket:
		return "'['"
	case tRBracket:
		return "']'"
	case tLBrace:
		return "'{'"
	case tRBrace:
		return "'}'"
	case tLAngle:
		return "'<'"
	case tRAngle:
		return "'>'"
	case tPipe:
		return "'|'"
	default:
		return "word"
	}
}

type lexTok struct {
	kind tokKind
	text string
}

// lexer tokenizes a template source string into structural tokens ((, ),
// [, ], {, }, <, >, |) and literal words. Word runs use the exact same
// isTokenRune/mapRune rules as Normalize, so a literal template word
// tokenizes identically to how the same text would tokenize as an
// utterance.
type lexer struct {
	src []rune
	pos int
}

func newLexer(src string) *lexer { return &lexer{src: []rune(src)} }

func (l *lexer) peek() lexTok {
	save := l.pos
	t := l.next()
	l.pos = save
	return t
}

func (l *lexer) next() lexTok {
	for l.pos < len(l.src) && isSpace(l.src[l.pos]) {
		l.pos++
	}
	if l.pos >= len(l.src) {
		return lexTok{kind: tEOF}
	}
	r := l.src[l.pos]
	switch r {
	case '(':
		l.pos++
		return lexTok{kind: tLParen}
	case ')':
		l.pos++
		return lexTok{kind: tRParen}
	case '[':
		l.pos++
		return lexTok{kind: tLBracket}
	case ']':
		l.pos++
		return lexTok{kind: tRBracket}
	case '{':
		l.pos++
		return lexTok{kind: tLBrace}
	case '}':
		l.pos++
		return lexTok{kind: tRBrace}
	case '<':
		l.pos++
		return lexTok{kind: tLAngle}
	case '>':
		l.pos++
		return lexTok{kind: tRAngle}
	case '|':
		l.pos++
		return lexTok{kind: tPipe}
	default:
		if !isTokenRune(mapRune(r)) {
			// Stray punctuation/separator outside a word: skip it, exactly
			// like Normalize treats it as a separator.
			l.pos++
			return l.next()
		}
		var b strings.Builder
		for l.pos < len(l.src) {
			c := l.src[l.pos]
			mc := mapRune(c)
			if !isTokenRune(mc) {
				break
			}
			b.WriteRune(mc)
			l.pos++
		}
		return lexTok{kind: tWord, text: b.String()}
	}
}

func isSpace(r rune) bool { return r == ' ' || r == '\t' || r == '\n' || r == '\r' }

// readUntil scans raw runes (bypassing word tokenization) up to and
// including closing, returning the trimmed text in between. It is used for
// {slot} and <rule> identifiers, which are read verbatim rather than
// word-tokenized.
func (l *lexer) readUntil(closing rune) (string, bool) {
	start := l.pos
	for l.pos < len(l.src) {
		if l.src[l.pos] == closing {
			s := strings.TrimSpace(string(l.src[start:l.pos]))
			l.pos++
			return s, true
		}
		l.pos++
	}
	return "", false
}

// parser is a recursive-descent parser over one template or rule source.
// expanding is shared across a whole expansion chain (a template and every
// rule it pulls in), so indirect rule recursion is caught the same way
// direct recursion is.
type parser struct {
	lex       *lexer
	origSrc   string
	rules     map[string]string
	slotTypes map[string]string
	expanding map[string]bool
}

// Compile parses src into a Template. rules supplies named grammar
// fragments referenced via <rule>; slotTypes maps every slot name used in
// src (or any rule it pulls in) to its declared type, which Compile needs
// to enforce that a "text" slot is only ever the last element of the
// sequence it appears in.
func Compile(src string, rules map[string]string, slotTypes map[string]string) (*Template, error) {
	p := &parser{
		lex:       newLexer(src),
		origSrc:   src,
		rules:     rules,
		slotTypes: slotTypes,
		expanding: map[string]bool{},
	}
	root, err := p.parseSeq(nil)
	if err != nil {
		return nil, err
	}
	if tok := p.lex.peek(); tok.kind != tEOF {
		return nil, fmt.Errorf("tmpl: %q: unbalanced %s", src, tok.kind)
	}
	if _, err := textFinalCheck(root); err != nil {
		return nil, fmt.Errorf("tmpl: %q: %w", src, err)
	}
	vocab := map[string]bool{}
	collectVocab(root, vocab)
	return &Template{Source: src, root: root, vocab: vocab}, nil
}

// parseSeq reads terms until it sees a token in stop (or end of input),
// returning a single node directly when the sequence has exactly one term.
func (p *parser) parseSeq(stop map[tokKind]bool) (node, error) {
	var items []node
	for {
		tok := p.lex.peek()
		if tok.kind == tEOF || stop[tok.kind] {
			break
		}
		n, err := p.parseTerm()
		if err != nil {
			return nil, err
		}
		items = append(items, n)
	}
	if len(items) == 1 {
		return items[0], nil
	}
	return seqNode{items: items}, nil
}

// parseAltBody reads one or more '|'-separated sequences, stopping at
// closeKind (the caller consumes the closing token itself).
func (p *parser) parseAltBody(closeKind tokKind) ([]node, error) {
	stop := map[tokKind]bool{closeKind: true, tPipe: true}
	var branches []node
	for {
		seq, err := p.parseSeq(stop)
		if err != nil {
			return nil, err
		}
		branches = append(branches, seq)
		if p.lex.peek().kind == tPipe {
			p.lex.next()
			continue
		}
		break
	}
	return branches, nil
}

func (p *parser) parseTerm() (node, error) {
	tok := p.lex.next()
	switch tok.kind {
	case tWord:
		return litNode{word: tok.text}, nil
	case tLParen:
		branches, err := p.parseAltBody(tRParen)
		if err != nil {
			return nil, err
		}
		if close := p.lex.next(); close.kind != tRParen {
			return nil, fmt.Errorf("tmpl: %q: unbalanced '('", p.origSrc)
		}
		if len(branches) == 1 {
			return branches[0], nil
		}
		return altNode{branches: branches}, nil
	case tLBracket:
		branches, err := p.parseAltBody(tRBracket)
		if err != nil {
			return nil, err
		}
		if close := p.lex.next(); close.kind != tRBracket {
			return nil, fmt.Errorf("tmpl: %q: unbalanced '['", p.origSrc)
		}
		return optNode{branches: branches}, nil
	case tLBrace:
		name, ok := p.lex.readUntil('}')
		if !ok {
			return nil, fmt.Errorf("tmpl: %q: unbalanced '{'", p.origSrc)
		}
		typ, declared := p.slotTypes[name]
		if !declared {
			return nil, fmt.Errorf("tmpl: %q: undeclared slot {%s}", p.origSrc, name)
		}
		isText := typ == "text"
		max := 4
		if isText {
			max = 60
		}
		return slotNode{name: name, min: 1, max: max, isText: isText}, nil
	case tLAngle:
		name, ok := p.lex.readUntil('>')
		if !ok {
			return nil, fmt.Errorf("tmpl: %q: unbalanced '<'", p.origSrc)
		}
		return p.expandRule(name)
	case tPipe:
		return nil, fmt.Errorf("tmpl: %q: unexpected '|'", p.origSrc)
	case tRParen, tRBracket, tRBrace, tRAngle:
		return nil, fmt.Errorf("tmpl: %q: unbalanced %s", p.origSrc, tok.kind)
	default:
		return nil, fmt.Errorf("tmpl: %q: unexpected token", p.origSrc)
	}
}

// expandRule inlines the named rule's grammar, rejecting a rule that
// (directly or indirectly) references itself.
func (p *parser) expandRule(name string) (node, error) {
	src, ok := p.rules[name]
	if !ok {
		return nil, fmt.Errorf("tmpl: %q: unknown rule <%s>", p.origSrc, name)
	}
	if p.expanding[name] {
		return nil, fmt.Errorf("tmpl: %q: recursive rule <%s>", p.origSrc, name)
	}
	p.expanding[name] = true
	defer delete(p.expanding, name)

	sub := &parser{
		lex:       newLexer(src),
		origSrc:   src,
		rules:     p.rules,
		slotTypes: p.slotTypes,
		expanding: p.expanding,
	}
	n, err := sub.parseSeq(nil)
	if err != nil {
		return nil, fmt.Errorf("tmpl: expanding rule <%s>: %w", name, err)
	}
	if tok := sub.lex.peek(); tok.kind != tEOF {
		return nil, fmt.Errorf("tmpl: expanding rule <%s>: unbalanced %s", name, tok.kind)
	}
	return n, nil
}

// textFinalCheck verifies that every "text"-typed slot is the last element
// of the sequence that contains it. endsWithText reports whether n, read to
// its end, could itself be (or end with) a text-slot capture — used so the
// check also catches a text slot buried inside an alternative or optional
// group that is itself followed by more content.
func textFinalCheck(n node) (endsWithText bool, err error) {
	switch v := n.(type) {
	case litNode:
		return false, nil
	case slotNode:
		return v.isText, nil
	case seqNode:
		for i, item := range v.items {
			end, err := textFinalCheck(item)
			if err != nil {
				return false, err
			}
			if end && i != len(v.items)-1 {
				return false, fmt.Errorf("text slot must be the last element of the template")
			}
			if i == len(v.items)-1 {
				return end, nil
			}
		}
		return false, nil
	case altNode:
		any := false
		for _, b := range v.branches {
			end, err := textFinalCheck(b)
			if err != nil {
				return false, err
			}
			any = any || end
		}
		return any, nil
	case optNode:
		any := false
		for _, b := range v.branches {
			end, err := textFinalCheck(b)
			if err != nil {
				return false, err
			}
			any = any || end
		}
		return any, nil
	default:
		return false, nil
	}
}

func collectVocab(n node, out map[string]bool) {
	switch v := n.(type) {
	case litNode:
		out[v.word] = true
	case seqNode:
		for _, item := range v.items {
			collectVocab(item, out)
		}
	case altNode:
		for _, b := range v.branches {
			collectVocab(b, out)
		}
	case optNode:
		for _, b := range v.branches {
			collectVocab(b, out)
		}
	}
}
