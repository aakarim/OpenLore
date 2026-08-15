package cmds

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// --- jq ---

func CmdJq(ctx CmdContext, args []string, w io.Writer, errW io.Writer, stdin io.Reader) int {
	rawOutput := false
	compact := false
	exitStatus := false
	slurp := false
	var filter string
	var files []string

	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") && len(a) > 1 && a[1] != '-' {
			for _, ch := range a[1:] {
				switch ch {
				case 'r':
					rawOutput = true
				case 'c':
					compact = true
				case 'e':
					exitStatus = true
				case 's':
					slurp = true
				}
			}
		} else if filter == "" {
			filter = a
		} else {
			files = append(files, a)
		}
	}

	if filter == "" {
		filter = "."
	}

	var inputData []byte
	if len(files) > 0 {
		p := ctx.Resolve(files[0])
		var err error
		inputData, err = ctx.FS().ReadFile(p)
		if err != nil {
			fmt.Fprintf(errW, "jq: %s: %s\n", files[0], err)
			return 2
		}
	} else if stdin != nil {
		inputData, _ = io.ReadAll(stdin)
	} else {
		fmt.Fprintln(errW, "jq: missing input")
		return 2
	}

	inputStr := strings.TrimSpace(string(inputData))
	if inputStr == "" {
		return 0
	}

	var inputs []interface{}
	if slurp {
		// Parse all JSON values and wrap in array
		dec := json.NewDecoder(strings.NewReader(inputStr))
		for dec.More() {
			var v interface{}
			if err := dec.Decode(&v); err != nil {
				fmt.Fprintf(errW, "jq: parse error: %s\n", err)
				return 2
			}
			inputs = append(inputs, v)
		}
		inputs = []interface{}{inputs}
	} else {
		// Parse potentially multiple JSON values
		dec := json.NewDecoder(strings.NewReader(inputStr))
		dec.UseNumber()
		for dec.More() {
			var v interface{}
			if err := dec.Decode(&v); err != nil {
				fmt.Fprintf(errW, "jq: parse error: %s\n", err)
				return 2
			}
			inputs = append(inputs, normalizeJSONNumbers(v))
		}
	}

	expr, err := jqParse(filter)
	if err != nil {
		fmt.Fprintf(errW, "jq: compile error: %s\n", err)
		return 3
	}

	hasOutput := false
	lastWasNull := false
	lastWasFalse := false

	for _, input := range inputs {
		results, err := jqEval(expr, input)
		if err != nil {
			fmt.Fprintf(errW, "jq: error: %s\n", err)
			return 5
		}
		for _, result := range results {
			hasOutput = true
			if result == nil {
				lastWasNull = true
			} else if b, ok := result.(bool); ok && !b {
				lastWasFalse = true
			}
			jqPrintValue(w, result, rawOutput, compact)
		}
	}

	if exitStatus && (!hasOutput || lastWasNull || lastWasFalse) {
		return 1
	}
	return 0
}

func normalizeJSONNumbers(v interface{}) interface{} {
	switch val := v.(type) {
	case json.Number:
		if i, err := val.Int64(); err == nil {
			return float64(i)
		}
		if f, err := val.Float64(); err == nil {
			return f
		}
		return val.String()
	case map[string]interface{}:
		for k, v2 := range val {
			val[k] = normalizeJSONNumbers(v2)
		}
		return val
	case []interface{}:
		for i, v2 := range val {
			val[i] = normalizeJSONNumbers(v2)
		}
		return val
	}
	return v
}

func jqPrintValue(w io.Writer, v interface{}, raw, compact bool) {
	if raw {
		if s, ok := v.(string); ok {
			fmt.Fprintln(w, s)
			return
		}
	}

	var data []byte
	if compact {
		data, _ = json.Marshal(v)
	} else {
		data, _ = json.MarshalIndent(v, "", "  ")
	}
	fmt.Fprintln(w, string(data))
}

// --- jq AST ---

type jqNodeType int

const (
	jqIdentity        jqNodeType = iota // .
	jqField                             // .foo
	jqIndex                             // .[0]
	jqSlice                             // .[2:5]
	jqIterator                          // .[]
	jqPipe                              // a | b
	jqComma                             // a, b
	jqLiteral                           // "str", 123, true, false, null
	jqArrayConstruct                    // [expr]
	jqObjectConstruct                   // {key: expr}
	jqFuncCall                          // length, keys, etc.
	jqComparison                        // ==, !=, <, >, <=, >=
	jqArith                             // +, -, *, /, %
	jqAnd                               // and
	jqOr                                // or
	jqNot                               // not
	jqIf                                // if-then-else-end
	jqTryCatch                          // try-catch
	jqRecurse                           // ..
	jqOptional                          // .foo?
	jqStringInterp                      // string interpolation
)

type jqNode struct {
	typ      jqNodeType
	value    interface{} // literal value, field name, func name, operator
	children []*jqNode
	kvPairs  []jqKV // for object construction
}

type jqKV struct {
	key   *jqNode
	value *jqNode
}

// --- jq Parser ---

func jqParse(filter string) (*jqNode, error) {
	filter = strings.TrimSpace(filter)
	if filter == "" || filter == "." {
		return &jqNode{typ: jqIdentity}, nil
	}
	tokens := jqTokenize(filter)
	node, _, err := jqParseExpr(tokens, 0)
	return node, err
}

type jqToken struct {
	typ   string // "dot", "ident", "number", "string", "op", "lbracket", "rbracket", "lbrace", "rbrace", "pipe", "comma", "colon", "semi", "lparen", "rparen", "question"
	value string
}

func jqTokenize(filter string) []jqToken {
	var tokens []jqToken
	i := 0
	for i < len(filter) {
		ch := filter[i]
		switch {
		case ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r':
			i++
		case ch == '.':
			if i+1 < len(filter) && filter[i+1] == '.' {
				tokens = append(tokens, jqToken{"dotdot", ".."})
				i += 2
			} else {
				tokens = append(tokens, jqToken{"dot", "."})
				i++
			}
		case ch == '|':
			tokens = append(tokens, jqToken{"pipe", "|"})
			i++
		case ch == ',':
			tokens = append(tokens, jqToken{"comma", ","})
			i++
		case ch == ':':
			tokens = append(tokens, jqToken{"colon", ":"})
			i++
		case ch == ';':
			tokens = append(tokens, jqToken{"semi", ";"})
			i++
		case ch == '?':
			tokens = append(tokens, jqToken{"question", "?"})
			i++
		case ch == '[':
			tokens = append(tokens, jqToken{"lbracket", "["})
			i++
		case ch == ']':
			tokens = append(tokens, jqToken{"rbracket", "]"})
			i++
		case ch == '{':
			tokens = append(tokens, jqToken{"lbrace", "{"})
			i++
		case ch == '}':
			tokens = append(tokens, jqToken{"rbrace", "}"})
			i++
		case ch == '(':
			tokens = append(tokens, jqToken{"lparen", "("})
			i++
		case ch == ')':
			tokens = append(tokens, jqToken{"rparen", ")"})
			i++
		case ch == '"':
			// String literal
			j := i + 1
			for j < len(filter) {
				if filter[j] == '\\' && j+1 < len(filter) {
					j += 2
					continue
				}
				if filter[j] == '"' {
					break
				}
				j++
			}
			if j < len(filter) {
				j++ // include closing quote
			}
			tokens = append(tokens, jqToken{"string", filter[i:j]})
			i = j
		case ch == '-' || (ch >= '0' && ch <= '9'):
			j := i
			if ch == '-' {
				j++
			}
			for j < len(filter) && ((filter[j] >= '0' && filter[j] <= '9') || filter[j] == '.') {
				j++
			}
			tokens = append(tokens, jqToken{"number", filter[i:j]})
			i = j
		case ch == '=' && i+1 < len(filter) && filter[i+1] == '=':
			tokens = append(tokens, jqToken{"op", "=="})
			i += 2
		case ch == '!' && i+1 < len(filter) && filter[i+1] == '=':
			tokens = append(tokens, jqToken{"op", "!="})
			i += 2
		case ch == '<' && i+1 < len(filter) && filter[i+1] == '=':
			tokens = append(tokens, jqToken{"op", "<="})
			i += 2
		case ch == '>' && i+1 < len(filter) && filter[i+1] == '=':
			tokens = append(tokens, jqToken{"op", ">="})
			i += 2
		case ch == '<':
			tokens = append(tokens, jqToken{"op", "<"})
			i++
		case ch == '>':
			tokens = append(tokens, jqToken{"op", ">"})
			i++
		case ch == '+':
			tokens = append(tokens, jqToken{"op", "+"})
			i++
		case ch == '-':
			tokens = append(tokens, jqToken{"op", "-"})
			i++
		case ch == '*':
			tokens = append(tokens, jqToken{"op", "*"})
			i++
		case ch == '/':
			// Check if followed by another / for comment, otherwise operator
			tokens = append(tokens, jqToken{"op", "/"})
			i++
		case ch == '%':
			tokens = append(tokens, jqToken{"op", "%"})
			i++
		case ch == '@':
			// Format string like @base64, @csv, etc.
			j := i + 1
			for j < len(filter) && ((filter[j] >= 'a' && filter[j] <= 'z') || (filter[j] >= '0' && filter[j] <= '9') || filter[j] == '_') {
				j++
			}
			tokens = append(tokens, jqToken{"format", filter[i:j]})
			i = j
		case (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || ch == '_':
			j := i
			for j < len(filter) && ((filter[j] >= 'a' && filter[j] <= 'z') || (filter[j] >= 'A' && filter[j] <= 'Z') || (filter[j] >= '0' && filter[j] <= '9') || filter[j] == '_') {
				j++
			}
			word := filter[i:j]
			tokens = append(tokens, jqToken{"ident", word})
			i = j
		default:
			i++ // skip unknown
		}
	}
	return tokens
}

func jqParseExpr(tokens []jqToken, pos int) (*jqNode, int, error) {
	return jqParsePipe(tokens, pos)
}

func jqParsePipe(tokens []jqToken, pos int) (*jqNode, int, error) {
	left, pos, err := jqParseComma(tokens, pos)
	if err != nil {
		return nil, pos, err
	}

	for pos < len(tokens) && tokens[pos].typ == "pipe" {
		pos++ // consume |
		right, newPos, err := jqParseComma(tokens, pos)
		if err != nil {
			return nil, newPos, err
		}
		left = &jqNode{typ: jqPipe, children: []*jqNode{left, right}}
		pos = newPos
	}

	return left, pos, nil
}

func jqParseComma(tokens []jqToken, pos int) (*jqNode, int, error) {
	left, pos, err := jqParseComparison(tokens, pos)
	if err != nil {
		return nil, pos, err
	}

	for pos < len(tokens) && tokens[pos].typ == "comma" {
		pos++ // consume ,
		right, newPos, err := jqParseComparison(tokens, pos)
		if err != nil {
			return nil, newPos, err
		}
		left = &jqNode{typ: jqComma, children: []*jqNode{left, right}}
		pos = newPos
	}

	return left, pos, nil
}

func jqParseComparison(tokens []jqToken, pos int) (*jqNode, int, error) {
	left, pos, err := jqParseArith(tokens, pos)
	if err != nil {
		return nil, pos, err
	}

	for pos < len(tokens) && tokens[pos].typ == "op" {
		op := tokens[pos].value
		if op == "==" || op == "!=" || op == "<" || op == ">" || op == "<=" || op == ">=" {
			pos++
			right, newPos, err := jqParseArith(tokens, pos)
			if err != nil {
				return nil, newPos, err
			}
			left = &jqNode{typ: jqComparison, value: op, children: []*jqNode{left, right}}
			pos = newPos
		} else {
			break
		}
	}

	// Handle "and" / "or" keywords
	for pos < len(tokens) && tokens[pos].typ == "ident" && (tokens[pos].value == "and" || tokens[pos].value == "or") {
		op := tokens[pos].value
		pos++
		right, newPos, err := jqParseArith(tokens, pos)
		if err != nil {
			return nil, newPos, err
		}
		if op == "and" {
			left = &jqNode{typ: jqAnd, children: []*jqNode{left, right}}
		} else {
			left = &jqNode{typ: jqOr, children: []*jqNode{left, right}}
		}
		pos = newPos
	}

	return left, pos, nil
}

func jqParseArith(tokens []jqToken, pos int) (*jqNode, int, error) {
	left, pos, err := jqParsePostfix(tokens, pos)
	if err != nil {
		return nil, pos, err
	}

	for pos < len(tokens) && tokens[pos].typ == "op" {
		op := tokens[pos].value
		if op == "+" || op == "-" || op == "*" || op == "/" || op == "%" {
			pos++
			right, newPos, err := jqParsePostfix(tokens, pos)
			if err != nil {
				return nil, newPos, err
			}
			left = &jqNode{typ: jqArith, value: op, children: []*jqNode{left, right}}
			pos = newPos
		} else {
			break
		}
	}

	return left, pos, nil
}

func jqParsePostfix(tokens []jqToken, pos int) (*jqNode, int, error) {
	node, pos, err := jqParsePrimary(tokens, pos)
	if err != nil {
		return nil, pos, err
	}

	for pos < len(tokens) {
		if tokens[pos].typ == "dot" && pos+1 < len(tokens) && tokens[pos+1].typ == "ident" {
			pos++ // consume .
			fieldName := tokens[pos].value
			pos++
			node = &jqNode{typ: jqField, value: fieldName, children: []*jqNode{node}}
			// Check for optional ?
			if pos < len(tokens) && tokens[pos].typ == "question" {
				node = &jqNode{typ: jqOptional, children: []*jqNode{node}}
				pos++
			}
		} else if tokens[pos].typ == "lbracket" {
			pos++ // consume [
			if pos < len(tokens) && tokens[pos].typ == "rbracket" {
				// .[]
				pos++
				node = &jqNode{typ: jqIterator, children: []*jqNode{node}}
			} else if pos < len(tokens) && tokens[pos].typ == "number" {
				idx := tokens[pos].value
				pos++
				if pos < len(tokens) && tokens[pos].typ == "colon" {
					// Slice [start:end]
					pos++ // consume :
					endIdx := ""
					if pos < len(tokens) && tokens[pos].typ == "number" {
						endIdx = tokens[pos].value
						pos++
					}
					if pos < len(tokens) && tokens[pos].typ == "rbracket" {
						pos++
					}
					node = &jqNode{typ: jqSlice, value: idx + ":" + endIdx, children: []*jqNode{node}}
				} else {
					if pos < len(tokens) && tokens[pos].typ == "rbracket" {
						pos++
					}
					node = &jqNode{typ: jqIndex, value: idx, children: []*jqNode{node}}
				}
			} else if pos < len(tokens) && tokens[pos].typ == "colon" {
				// Slice [:end]
				pos++ // consume :
				endIdx := ""
				if pos < len(tokens) && tokens[pos].typ == "number" {
					endIdx = tokens[pos].value
					pos++
				}
				if pos < len(tokens) && tokens[pos].typ == "rbracket" {
					pos++
				}
				node = &jqNode{typ: jqSlice, value: ":" + endIdx, children: []*jqNode{node}}
			} else if pos < len(tokens) && tokens[pos].typ == "string" {
				// .["key"]
				key := jqUnquoteString(tokens[pos].value)
				pos++
				if pos < len(tokens) && tokens[pos].typ == "rbracket" {
					pos++
				}
				node = &jqNode{typ: jqField, value: key, children: []*jqNode{node}}
			} else {
				// Complex index expression
				inner, newPos, err := jqParseExpr(tokens, pos)
				if err != nil {
					return nil, newPos, err
				}
				pos = newPos
				if pos < len(tokens) && tokens[pos].typ == "rbracket" {
					pos++
				}
				node = &jqNode{typ: jqIndex, children: []*jqNode{node, inner}}
			}
		} else if tokens[pos].typ == "question" {
			node = &jqNode{typ: jqOptional, children: []*jqNode{node}}
			pos++
		} else {
			break
		}
	}

	return node, pos, nil
}

func jqParsePrimary(tokens []jqToken, pos int) (*jqNode, int, error) {
	if pos >= len(tokens) {
		return &jqNode{typ: jqIdentity}, pos, nil
	}

	tok := tokens[pos]

	switch tok.typ {
	case "dot":
		pos++
		if pos < len(tokens) && tokens[pos].typ == "ident" {
			fieldName := tokens[pos].value
			pos++
			node := &jqNode{typ: jqField, value: fieldName, children: []*jqNode{{typ: jqIdentity}}}
			if pos < len(tokens) && tokens[pos].typ == "question" {
				node = &jqNode{typ: jqOptional, children: []*jqNode{node}}
				pos++
			}
			return node, pos, nil
		}
		if pos < len(tokens) && tokens[pos].typ == "lbracket" {
			pos++ // consume [
			if pos < len(tokens) && tokens[pos].typ == "rbracket" {
				pos++
				return &jqNode{typ: jqIterator, children: []*jqNode{{typ: jqIdentity}}}, pos, nil
			}
			if pos < len(tokens) && tokens[pos].typ == "number" {
				idx := tokens[pos].value
				pos++
				if pos < len(tokens) && tokens[pos].typ == "colon" {
					pos++
					endIdx := ""
					if pos < len(tokens) && tokens[pos].typ == "number" {
						endIdx = tokens[pos].value
						pos++
					}
					if pos < len(tokens) && tokens[pos].typ == "rbracket" {
						pos++
					}
					return &jqNode{typ: jqSlice, value: idx + ":" + endIdx, children: []*jqNode{{typ: jqIdentity}}}, pos, nil
				}
				if pos < len(tokens) && tokens[pos].typ == "rbracket" {
					pos++
				}
				return &jqNode{typ: jqIndex, value: idx, children: []*jqNode{{typ: jqIdentity}}}, pos, nil
			}
			// .[expr]
			inner, newPos, err := jqParseExpr(tokens, pos)
			if err != nil {
				return nil, newPos, err
			}
			pos = newPos
			if pos < len(tokens) && tokens[pos].typ == "rbracket" {
				pos++
			}
			return &jqNode{typ: jqIndex, children: []*jqNode{{typ: jqIdentity}, inner}}, pos, nil
		}
		return &jqNode{typ: jqIdentity}, pos, nil

	case "dotdot":
		pos++
		return &jqNode{typ: jqRecurse}, pos, nil

	case "number":
		pos++
		f, _ := strconv.ParseFloat(tok.value, 64)
		return &jqNode{typ: jqLiteral, value: f}, pos, nil

	case "string":
		pos++
		return &jqNode{typ: jqLiteral, value: jqUnquoteString(tok.value)}, pos, nil

	case "ident":
		switch tok.value {
		case "null":
			pos++
			return &jqNode{typ: jqLiteral, value: nil}, pos, nil
		case "true":
			pos++
			return &jqNode{typ: jqLiteral, value: true}, pos, nil
		case "false":
			pos++
			return &jqNode{typ: jqLiteral, value: false}, pos, nil
		case "not":
			pos++
			return &jqNode{typ: jqNot}, pos, nil
		case "empty":
			pos++
			return &jqNode{typ: jqFuncCall, value: "empty"}, pos, nil
		case "if":
			return jqParseIf(tokens, pos)
		case "try":
			return jqParseTry(tokens, pos)
		default:
			// Function call or just identifier
			name := tok.value
			pos++
			// Check if followed by (args)
			if pos < len(tokens) && tokens[pos].typ == "lparen" {
				pos++ // consume (
				var fnArgs []*jqNode
				for pos < len(tokens) && tokens[pos].typ != "rparen" {
					arg, newPos, err := jqParseExpr(tokens, pos)
					if err != nil {
						return nil, newPos, err
					}
					fnArgs = append(fnArgs, arg)
					pos = newPos
					if pos < len(tokens) && tokens[pos].typ == "semi" {
						pos++ // consume ;
					} else if pos < len(tokens) && tokens[pos].typ == "comma" {
						pos++ // consume ,
					}
				}
				if pos < len(tokens) && tokens[pos].typ == "rparen" {
					pos++
				}
				return &jqNode{typ: jqFuncCall, value: name, children: fnArgs}, pos, nil
			}
			return &jqNode{typ: jqFuncCall, value: name}, pos, nil
		}

	case "lbracket":
		// Array construction: [expr]
		pos++ // consume [
		if pos < len(tokens) && tokens[pos].typ == "rbracket" {
			pos++
			return &jqNode{typ: jqArrayConstruct}, pos, nil
		}
		inner, newPos, err := jqParseExpr(tokens, pos)
		if err != nil {
			return nil, newPos, err
		}
		pos = newPos
		if pos < len(tokens) && tokens[pos].typ == "rbracket" {
			pos++
		}
		return &jqNode{typ: jqArrayConstruct, children: []*jqNode{inner}}, pos, nil

	case "lbrace":
		// Object construction: {key: expr, ...}
		return jqParseObject(tokens, pos)

	case "lparen":
		pos++ // consume (
		inner, newPos, err := jqParseExpr(tokens, pos)
		if err != nil {
			return nil, newPos, err
		}
		pos = newPos
		if pos < len(tokens) && tokens[pos].typ == "rparen" {
			pos++
		}
		return inner, pos, nil

	case "format":
		// @base64, @csv, etc.
		pos++
		return &jqNode{typ: jqFuncCall, value: tok.value}, pos, nil

	default:
		return &jqNode{typ: jqIdentity}, pos + 1, nil
	}
}

func jqParseIf(tokens []jqToken, pos int) (*jqNode, int, error) {
	pos++ // consume "if"
	cond, pos, err := jqParseExpr(tokens, pos)
	if err != nil {
		return nil, pos, err
	}
	// consume "then"
	if pos < len(tokens) && tokens[pos].typ == "ident" && tokens[pos].value == "then" {
		pos++
	}
	thenExpr, pos, err := jqParseExpr(tokens, pos)
	if err != nil {
		return nil, pos, err
	}
	var elseExpr *jqNode
	if pos < len(tokens) && tokens[pos].typ == "ident" && tokens[pos].value == "else" {
		pos++
		elseExpr, pos, err = jqParseExpr(tokens, pos)
		if err != nil {
			return nil, pos, err
		}
	}
	if pos < len(tokens) && tokens[pos].typ == "ident" && tokens[pos].value == "end" {
		pos++
	}
	children := []*jqNode{cond, thenExpr}
	if elseExpr != nil {
		children = append(children, elseExpr)
	}
	return &jqNode{typ: jqIf, children: children}, pos, nil
}

func jqParseTry(tokens []jqToken, pos int) (*jqNode, int, error) {
	pos++ // consume "try"
	expr, pos, err := jqParseExpr(tokens, pos)
	if err != nil {
		return nil, pos, err
	}
	var catchExpr *jqNode
	if pos < len(tokens) && tokens[pos].typ == "ident" && tokens[pos].value == "catch" {
		pos++
		catchExpr, pos, err = jqParseExpr(tokens, pos)
		if err != nil {
			return nil, pos, err
		}
	}
	children := []*jqNode{expr}
	if catchExpr != nil {
		children = append(children, catchExpr)
	}
	return &jqNode{typ: jqTryCatch, children: children}, pos, nil
}

func jqParseObject(tokens []jqToken, pos int) (*jqNode, int, error) {
	pos++ // consume {
	node := &jqNode{typ: jqObjectConstruct}

	for pos < len(tokens) && tokens[pos].typ != "rbrace" {
		// Parse key
		var keyNode *jqNode
		if tokens[pos].typ == "string" {
			keyNode = &jqNode{typ: jqLiteral, value: jqUnquoteString(tokens[pos].value)}
			pos++
		} else if tokens[pos].typ == "ident" {
			keyNode = &jqNode{typ: jqLiteral, value: tokens[pos].value}
			pos++
		} else if tokens[pos].typ == "lparen" {
			// Computed key: (expr)
			pos++
			keyExpr, newPos, err := jqParseExpr(tokens, pos)
			if err != nil {
				return nil, newPos, err
			}
			pos = newPos
			if pos < len(tokens) && tokens[pos].typ == "rparen" {
				pos++
			}
			keyNode = keyExpr
		} else if tokens[pos].typ == "dot" {
			// .field shorthand as key
			keyExpr, newPos, err := jqParsePrimary(tokens, pos)
			if err != nil {
				return nil, newPos, err
			}
			pos = newPos
			// Use field name as both key and value
			if keyExpr.typ == jqField {
				kv := jqKV{
					key:   &jqNode{typ: jqLiteral, value: keyExpr.value},
					value: keyExpr,
				}
				node.kvPairs = append(node.kvPairs, kv)
				if pos < len(tokens) && tokens[pos].typ == "comma" {
					pos++
				}
				continue
			}
		} else {
			break
		}

		// Colon and value
		if pos < len(tokens) && tokens[pos].typ == "colon" {
			pos++
			valExpr, newPos, err := jqParseComparison(tokens, pos)
			if err != nil {
				return nil, newPos, err
			}
			pos = newPos
			node.kvPairs = append(node.kvPairs, jqKV{key: keyNode, value: valExpr})
		} else {
			// Shorthand: {name} means {name: .name}
			if keyNode.typ == jqLiteral {
				if s, ok := keyNode.value.(string); ok {
					node.kvPairs = append(node.kvPairs, jqKV{
						key:   keyNode,
						value: &jqNode{typ: jqField, value: s, children: []*jqNode{{typ: jqIdentity}}},
					})
				}
			}
		}

		if pos < len(tokens) && tokens[pos].typ == "comma" {
			pos++
		}
	}

	if pos < len(tokens) && tokens[pos].typ == "rbrace" {
		pos++
	}
	return node, pos, nil
}

func jqUnquoteString(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		s = s[1 : len(s)-1]
	}
	s = strings.ReplaceAll(s, "\\\"", "\"")
	s = strings.ReplaceAll(s, "\\n", "\n")
	s = strings.ReplaceAll(s, "\\t", "\t")
	s = strings.ReplaceAll(s, "\\\\", "\\")
	return s
}

// --- jq Evaluator ---

func jqEval(node *jqNode, input interface{}) ([]interface{}, error) {
	if node == nil {
		return []interface{}{input}, nil
	}

	switch node.typ {
	case jqIdentity:
		return []interface{}{input}, nil

	case jqField:
		var base interface{} = input
		if len(node.children) > 0 {
			bases, err := jqEval(node.children[0], input)
			if err != nil {
				return nil, err
			}
			var results []interface{}
			for _, b := range bases {
				fieldName, _ := node.value.(string)
				val := jqAccessField(b, fieldName)
				results = append(results, val)
			}
			return results, nil
		}
		fieldName, _ := node.value.(string)
		return []interface{}{jqAccessField(base, fieldName)}, nil

	case jqIndex:
		var base interface{} = input
		if len(node.children) > 0 {
			bases, err := jqEval(node.children[0], input)
			if err != nil {
				return nil, err
			}
			if len(bases) > 0 {
				base = bases[0]
			}
		}
		// If value is a string index
		if idxStr, ok := node.value.(string); ok {
			idx, err := strconv.Atoi(idxStr)
			if err != nil {
				return nil, fmt.Errorf("invalid index: %s", idxStr)
			}
			arr, ok := base.([]interface{})
			if !ok {
				return []interface{}{nil}, nil
			}
			if idx < 0 {
				idx = len(arr) + idx
			}
			if idx >= 0 && idx < len(arr) {
				return []interface{}{arr[idx]}, nil
			}
			return []interface{}{nil}, nil
		}
		// Dynamic index expression
		if len(node.children) >= 2 {
			idxVals, err := jqEval(node.children[1], input)
			if err != nil {
				return nil, err
			}
			if len(idxVals) > 0 {
				arr, ok := base.([]interface{})
				if !ok {
					return []interface{}{nil}, nil
				}
				idx := jqToInt(idxVals[0])
				if idx < 0 {
					idx = len(arr) + idx
				}
				if idx >= 0 && idx < len(arr) {
					return []interface{}{arr[idx]}, nil
				}
			}
			return []interface{}{nil}, nil
		}
		return []interface{}{nil}, nil

	case jqSlice:
		var base interface{} = input
		if len(node.children) > 0 {
			bases, err := jqEval(node.children[0], input)
			if err != nil {
				return nil, err
			}
			if len(bases) > 0 {
				base = bases[0]
			}
		}
		arr, ok := base.([]interface{})
		if !ok {
			return []interface{}{nil}, nil
		}
		parts := strings.SplitN(node.value.(string), ":", 2)
		start := 0
		end := len(arr)
		if parts[0] != "" {
			start, _ = strconv.Atoi(parts[0])
		}
		if len(parts) > 1 && parts[1] != "" {
			end, _ = strconv.Atoi(parts[1])
		}
		if start < 0 {
			start = len(arr) + start
		}
		if end < 0 {
			end = len(arr) + end
		}
		if start < 0 {
			start = 0
		}
		if end > len(arr) {
			end = len(arr)
		}
		if start >= end {
			return []interface{}{[]interface{}{}}, nil
		}
		return []interface{}{arr[start:end]}, nil

	case jqIterator:
		var base interface{} = input
		if len(node.children) > 0 {
			bases, err := jqEval(node.children[0], input)
			if err != nil {
				return nil, err
			}
			var results []interface{}
			for _, b := range bases {
				iter, err := jqIterateValue(b)
				if err != nil {
					return nil, err
				}
				results = append(results, iter...)
			}
			return results, nil
		}
		return jqIterateValue(base)

	case jqPipe:
		results, err := jqEval(node.children[0], input)
		if err != nil {
			return nil, err
		}
		var finalResults []interface{}
		for _, r := range results {
			next, err := jqEval(node.children[1], r)
			if err != nil {
				return nil, err
			}
			finalResults = append(finalResults, next...)
		}
		return finalResults, nil

	case jqComma:
		left, err := jqEval(node.children[0], input)
		if err != nil {
			return nil, err
		}
		right, err := jqEval(node.children[1], input)
		if err != nil {
			return nil, err
		}
		return append(left, right...), nil

	case jqLiteral:
		return []interface{}{node.value}, nil

	case jqArrayConstruct:
		if len(node.children) == 0 {
			return []interface{}{[]interface{}{}}, nil
		}
		inner, err := jqEval(node.children[0], input)
		if err != nil {
			return nil, err
		}
		return []interface{}{inner}, nil

	case jqObjectConstruct:
		obj := make(map[string]interface{})
		for _, kv := range node.kvPairs {
			keys, err := jqEval(kv.key, input)
			if err != nil {
				return nil, err
			}
			vals, err := jqEval(kv.value, input)
			if err != nil {
				return nil, err
			}
			key := ""
			if len(keys) > 0 {
				key = jqToString(keys[0])
			}
			var val interface{}
			if len(vals) > 0 {
				val = vals[0]
			}
			obj[key] = val
		}
		return []interface{}{obj}, nil

	case jqComparison:
		leftVals, err := jqEval(node.children[0], input)
		if err != nil {
			return nil, err
		}
		rightVals, err := jqEval(node.children[1], input)
		if err != nil {
			return nil, err
		}
		var l, r interface{}
		if len(leftVals) > 0 {
			l = leftVals[0]
		}
		if len(rightVals) > 0 {
			r = rightVals[0]
		}
		result := jqCompare(l, r, node.value.(string))
		return []interface{}{result}, nil

	case jqArith:
		leftVals, err := jqEval(node.children[0], input)
		if err != nil {
			return nil, err
		}
		rightVals, err := jqEval(node.children[1], input)
		if err != nil {
			return nil, err
		}
		var l, r interface{}
		if len(leftVals) > 0 {
			l = leftVals[0]
		}
		if len(rightVals) > 0 {
			r = rightVals[0]
		}
		result := jqArithOp(l, r, node.value.(string))
		return []interface{}{result}, nil

	case jqAnd:
		leftVals, err := jqEval(node.children[0], input)
		if err != nil {
			return nil, err
		}
		rightVals, err := jqEval(node.children[1], input)
		if err != nil {
			return nil, err
		}
		l := len(leftVals) > 0 && jqIsTruthy(leftVals[0])
		r := len(rightVals) > 0 && jqIsTruthy(rightVals[0])
		return []interface{}{l && r}, nil

	case jqOr:
		leftVals, err := jqEval(node.children[0], input)
		if err != nil {
			return nil, err
		}
		rightVals, err := jqEval(node.children[1], input)
		if err != nil {
			return nil, err
		}
		l := len(leftVals) > 0 && jqIsTruthy(leftVals[0])
		r := len(rightVals) > 0 && jqIsTruthy(rightVals[0])
		return []interface{}{l || r}, nil

	case jqNot:
		return []interface{}{!jqIsTruthy(input)}, nil

	case jqIf:
		condVals, err := jqEval(node.children[0], input)
		if err != nil {
			return nil, err
		}
		cond := len(condVals) > 0 && jqIsTruthy(condVals[0])
		if cond {
			return jqEval(node.children[1], input)
		}
		if len(node.children) > 2 {
			return jqEval(node.children[2], input)
		}
		return []interface{}{input}, nil

	case jqTryCatch:
		results, err := jqEval(node.children[0], input)
		if err != nil {
			if len(node.children) > 1 {
				return jqEval(node.children[1], input)
			}
			return nil, nil // try without catch: suppress error
		}
		return results, nil

	case jqRecurse:
		return jqRecurseValues(input), nil

	case jqOptional:
		results, err := jqEval(node.children[0], input)
		if err != nil {
			return nil, nil
		}
		return results, nil

	case jqFuncCall:
		return jqCallBuiltin(node, input)
	}

	return []interface{}{input}, nil
}

func jqAccessField(obj interface{}, field string) interface{} {
	if obj == nil {
		return nil
	}
	m, ok := obj.(map[string]interface{})
	if !ok {
		return nil
	}
	return m[field]
}

func jqIterateValue(v interface{}) ([]interface{}, error) {
	switch val := v.(type) {
	case []interface{}:
		return val, nil
	case map[string]interface{}:
		var results []interface{}
		keys := make([]string, 0, len(val))
		for k := range val {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			results = append(results, val[k])
		}
		return results, nil
	default:
		return nil, fmt.Errorf("cannot iterate over %T", v)
	}
}

func jqToString(v interface{}) string {
	if v == nil {
		return "null"
	}
	switch val := v.(type) {
	case string:
		return val
	case float64:
		if val == math.Trunc(val) {
			return strconv.FormatInt(int64(val), 10)
		}
		return strconv.FormatFloat(val, 'f', -1, 64)
	case bool:
		if val {
			return "true"
		}
		return "false"
	default:
		data, _ := json.Marshal(v)
		return string(data)
	}
}

func jqToFloat(v interface{}) float64 {
	switch val := v.(type) {
	case float64:
		return val
	case string:
		f, _ := strconv.ParseFloat(val, 64)
		return f
	case bool:
		if val {
			return 1
		}
		return 0
	}
	return 0
}

func jqToInt(v interface{}) int {
	return int(jqToFloat(v))
}

func jqIsTruthy(v interface{}) bool {
	if v == nil {
		return false
	}
	if b, ok := v.(bool); ok {
		return b
	}
	return true
}

func jqCompare(l, r interface{}, op string) bool {
	lf, lok := l.(float64)
	rf, rok := r.(float64)
	if lok && rok {
		switch op {
		case "==":
			return lf == rf
		case "!=":
			return lf != rf
		case "<":
			return lf < rf
		case ">":
			return lf > rf
		case "<=":
			return lf <= rf
		case ">=":
			return lf >= rf
		}
	}

	ls := jqToString(l)
	rs := jqToString(r)
	switch op {
	case "==":
		return jqDeepEqual(l, r)
	case "!=":
		return !jqDeepEqual(l, r)
	case "<":
		return ls < rs
	case ">":
		return ls > rs
	case "<=":
		return ls <= rs
	case ">=":
		return ls >= rs
	}
	return false
}

func jqDeepEqual(a, b interface{}) bool {
	aj, _ := json.Marshal(a)
	bj, _ := json.Marshal(b)
	return string(aj) == string(bj)
}

func jqArithOp(l, r interface{}, op string) interface{} {
	// String concatenation with +
	if op == "+" {
		ls, lok := l.(string)
		rs, rok := r.(string)
		if lok && rok {
			return ls + rs
		}
		la, lok := l.([]interface{})
		ra, rok := r.([]interface{})
		if lok && rok {
			return append(la, ra...)
		}
		lm, lok := l.(map[string]interface{})
		rm, rok := r.(map[string]interface{})
		if lok && rok {
			result := make(map[string]interface{})
			for k, v := range lm {
				result[k] = v
			}
			for k, v := range rm {
				result[k] = v
			}
			return result
		}
	}

	lf := jqToFloat(l)
	rf := jqToFloat(r)
	switch op {
	case "+":
		return lf + rf
	case "-":
		return lf - rf
	case "*":
		return lf * rf
	case "/":
		if rf == 0 {
			return nil
		}
		return lf / rf
	case "%":
		if rf == 0 {
			return nil
		}
		return math.Mod(lf, rf)
	}
	return nil
}

func jqRecurseValues(v interface{}) []interface{} {
	results := []interface{}{v}
	switch val := v.(type) {
	case map[string]interface{}:
		keys := make([]string, 0, len(val))
		for k := range val {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			results = append(results, jqRecurseValues(val[k])...)
		}
	case []interface{}:
		for _, item := range val {
			results = append(results, jqRecurseValues(item)...)
		}
	}
	return results
}

func jqCallBuiltin(node *jqNode, input interface{}) ([]interface{}, error) {
	name := node.value.(string)

	switch name {
	case "length":
		switch val := input.(type) {
		case string:
			return []interface{}{float64(len(val))}, nil
		case []interface{}:
			return []interface{}{float64(len(val))}, nil
		case map[string]interface{}:
			return []interface{}{float64(len(val))}, nil
		case nil:
			return []interface{}{float64(0)}, nil
		}
		return []interface{}{float64(0)}, nil

	case "keys", "keys_unsorted":
		m, ok := input.(map[string]interface{})
		if !ok {
			if arr, ok := input.([]interface{}); ok {
				var indices []interface{}
				for i := range arr {
					indices = append(indices, float64(i))
				}
				return []interface{}{indices}, nil
			}
			return nil, fmt.Errorf("null has no keys")
		}
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		if name == "keys" {
			sort.Strings(keys)
		}
		var result []interface{}
		for _, k := range keys {
			result = append(result, k)
		}
		return []interface{}{result}, nil

	case "values":
		m, ok := input.(map[string]interface{})
		if !ok {
			if arr, ok := input.([]interface{}); ok {
				return []interface{}{arr}, nil
			}
			return nil, fmt.Errorf("null has no values")
		}
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var result []interface{}
		for _, k := range keys {
			result = append(result, m[k])
		}
		return []interface{}{result}, nil

	case "has":
		if len(node.children) == 0 {
			return []interface{}{false}, nil
		}
		keyVals, err := jqEval(node.children[0], input)
		if err != nil {
			return nil, err
		}
		key := ""
		if len(keyVals) > 0 {
			key = jqToString(keyVals[0])
		}
		m, ok := input.(map[string]interface{})
		if !ok {
			return []interface{}{false}, nil
		}
		_, exists := m[key]
		return []interface{}{exists}, nil

	case "type":
		return []interface{}{jqTypeName(input)}, nil

	case "tostring":
		return []interface{}{jqToString(input)}, nil

	case "tonumber":
		return []interface{}{jqToFloat(input)}, nil

	case "ascii_downcase":
		if s, ok := input.(string); ok {
			return []interface{}{strings.ToLower(s)}, nil
		}
		return []interface{}{input}, nil

	case "ascii_upcase":
		if s, ok := input.(string); ok {
			return []interface{}{strings.ToUpper(s)}, nil
		}
		return []interface{}{input}, nil

	case "ltrimstr":
		if len(node.children) > 0 {
			vals, _ := jqEval(node.children[0], input)
			if len(vals) > 0 {
				if s, ok := input.(string); ok {
					return []interface{}{strings.TrimPrefix(s, jqToString(vals[0]))}, nil
				}
			}
		}
		return []interface{}{input}, nil

	case "rtrimstr":
		if len(node.children) > 0 {
			vals, _ := jqEval(node.children[0], input)
			if len(vals) > 0 {
				if s, ok := input.(string); ok {
					return []interface{}{strings.TrimSuffix(s, jqToString(vals[0]))}, nil
				}
			}
		}
		return []interface{}{input}, nil

	case "startswith":
		if len(node.children) > 0 {
			vals, _ := jqEval(node.children[0], input)
			if len(vals) > 0 {
				if s, ok := input.(string); ok {
					return []interface{}{strings.HasPrefix(s, jqToString(vals[0]))}, nil
				}
			}
		}
		return []interface{}{false}, nil

	case "endswith":
		if len(node.children) > 0 {
			vals, _ := jqEval(node.children[0], input)
			if len(vals) > 0 {
				if s, ok := input.(string); ok {
					return []interface{}{strings.HasSuffix(s, jqToString(vals[0]))}, nil
				}
			}
		}
		return []interface{}{false}, nil

	case "split":
		if len(node.children) > 0 {
			vals, _ := jqEval(node.children[0], input)
			if len(vals) > 0 {
				if s, ok := input.(string); ok {
					parts := strings.Split(s, jqToString(vals[0]))
					var result []interface{}
					for _, p := range parts {
						result = append(result, p)
					}
					return []interface{}{result}, nil
				}
			}
		}
		return []interface{}{input}, nil

	case "join":
		if len(node.children) > 0 {
			vals, _ := jqEval(node.children[0], input)
			if len(vals) > 0 {
				if arr, ok := input.([]interface{}); ok {
					sep := jqToString(vals[0])
					var parts []string
					for _, item := range arr {
						parts = append(parts, jqToString(item))
					}
					return []interface{}{strings.Join(parts, sep)}, nil
				}
			}
		}
		return []interface{}{input}, nil

	case "test":
		if len(node.children) > 0 {
			vals, _ := jqEval(node.children[0], input)
			if len(vals) > 0 {
				if s, ok := input.(string); ok {
					re, err := regexp.Compile(jqToString(vals[0]))
					if err != nil {
						return []interface{}{false}, nil
					}
					return []interface{}{re.MatchString(s)}, nil
				}
			}
		}
		return []interface{}{false}, nil

	case "gsub":
		if len(node.children) >= 2 {
			patVals, _ := jqEval(node.children[0], input)
			repVals, _ := jqEval(node.children[1], input)
			if len(patVals) > 0 && len(repVals) > 0 {
				if s, ok := input.(string); ok {
					re, err := regexp.Compile(jqToString(patVals[0]))
					if err != nil {
						return []interface{}{s}, nil
					}
					return []interface{}{re.ReplaceAllString(s, jqToString(repVals[0]))}, nil
				}
			}
		}
		return []interface{}{input}, nil

	case "sub":
		if len(node.children) >= 2 {
			patVals, _ := jqEval(node.children[0], input)
			repVals, _ := jqEval(node.children[1], input)
			if len(patVals) > 0 && len(repVals) > 0 {
				if s, ok := input.(string); ok {
					re, err := regexp.Compile(jqToString(patVals[0]))
					if err != nil {
						return []interface{}{s}, nil
					}
					loc := re.FindStringIndex(s)
					if loc != nil {
						return []interface{}{s[:loc[0]] + jqToString(repVals[0]) + s[loc[1]:]}, nil
					}
					return []interface{}{s}, nil
				}
			}
		}
		return []interface{}{input}, nil

	case "contains":
		if len(node.children) > 0 {
			vals, _ := jqEval(node.children[0], input)
			if len(vals) > 0 {
				if s, ok := input.(string); ok {
					return []interface{}{strings.Contains(s, jqToString(vals[0]))}, nil
				}
				return []interface{}{jqContains(input, vals[0])}, nil
			}
		}
		return []interface{}{false}, nil

	case "select":
		if len(node.children) > 0 {
			condVals, err := jqEval(node.children[0], input)
			if err != nil {
				return nil, nil
			}
			if len(condVals) > 0 && jqIsTruthy(condVals[0]) {
				return []interface{}{input}, nil
			}
			return nil, nil // produce no output
		}
		return []interface{}{input}, nil

	case "empty":
		return nil, nil

	case "map":
		if len(node.children) > 0 {
			arr, ok := input.([]interface{})
			if !ok {
				return nil, fmt.Errorf("cannot map over %s", jqTypeName(input))
			}
			var results []interface{}
			for _, item := range arr {
				vals, err := jqEval(node.children[0], item)
				if err != nil {
					return nil, err
				}
				results = append(results, vals...)
			}
			return []interface{}{results}, nil
		}
		return []interface{}{input}, nil

	case "add":
		arr, ok := input.([]interface{})
		if !ok || len(arr) == 0 {
			return []interface{}{nil}, nil
		}
		result := arr[0]
		for _, item := range arr[1:] {
			result = jqArithOp(result, item, "+")
		}
		return []interface{}{result}, nil

	case "any":
		if arr, ok := input.([]interface{}); ok {
			for _, item := range arr {
				if jqIsTruthy(item) {
					return []interface{}{true}, nil
				}
			}
		}
		return []interface{}{false}, nil

	case "all":
		if arr, ok := input.([]interface{}); ok {
			for _, item := range arr {
				if !jqIsTruthy(item) {
					return []interface{}{false}, nil
				}
			}
			return []interface{}{true}, nil
		}
		return []interface{}{true}, nil

	case "flatten":
		if arr, ok := input.([]interface{}); ok {
			return []interface{}{jqFlatten(arr)}, nil
		}
		return []interface{}{input}, nil

	case "range":
		if len(node.children) > 0 {
			vals, _ := jqEval(node.children[0], input)
			if len(vals) > 0 {
				n := jqToInt(vals[0])
				var results []interface{}
				for i := 0; i < n; i++ {
					results = append(results, float64(i))
				}
				return results, nil
			}
		}
		return nil, nil

	case "floor":
		if f, ok := input.(float64); ok {
			return []interface{}{math.Floor(f)}, nil
		}
		return []interface{}{input}, nil

	case "ceil":
		if f, ok := input.(float64); ok {
			return []interface{}{math.Ceil(f)}, nil
		}
		return []interface{}{input}, nil

	case "round":
		if f, ok := input.(float64); ok {
			return []interface{}{math.Round(f)}, nil
		}
		return []interface{}{input}, nil

	case "sort":
		if arr, ok := input.([]interface{}); ok {
			sorted := make([]interface{}, len(arr))
			copy(sorted, arr)
			jqSortArray(sorted)
			return []interface{}{sorted}, nil
		}
		return []interface{}{input}, nil

	case "sort_by":
		if arr, ok := input.([]interface{}); ok && len(node.children) > 0 {
			sorted := make([]interface{}, len(arr))
			copy(sorted, arr)
			sort.SliceStable(sorted, func(i, j int) bool {
				vi, _ := jqEval(node.children[0], sorted[i])
				vj, _ := jqEval(node.children[0], sorted[j])
				var a, b interface{}
				if len(vi) > 0 {
					a = vi[0]
				}
				if len(vj) > 0 {
					b = vj[0]
				}
				return jqLessThan(a, b)
			})
			return []interface{}{sorted}, nil
		}
		return []interface{}{input}, nil

	case "group_by":
		if arr, ok := input.([]interface{}); ok && len(node.children) > 0 {
			groups := make(map[string][]interface{})
			var keys []string
			for _, item := range arr {
				vals, _ := jqEval(node.children[0], item)
				var keyVal interface{}
				if len(vals) > 0 {
					keyVal = vals[0]
				}
				key := jqToString(keyVal)
				if _, exists := groups[key]; !exists {
					keys = append(keys, key)
				}
				groups[key] = append(groups[key], item)
			}
			var result []interface{}
			for _, k := range keys {
				result = append(result, groups[k])
			}
			return []interface{}{result}, nil
		}
		return []interface{}{input}, nil

	case "unique":
		if arr, ok := input.([]interface{}); ok {
			seen := make(map[string]bool)
			var result []interface{}
			for _, item := range arr {
				key := jqToString(item)
				if !seen[key] {
					seen[key] = true
					result = append(result, item)
				}
			}
			return []interface{}{result}, nil
		}
		return []interface{}{input}, nil

	case "unique_by":
		if arr, ok := input.([]interface{}); ok && len(node.children) > 0 {
			seen := make(map[string]bool)
			var result []interface{}
			for _, item := range arr {
				vals, _ := jqEval(node.children[0], item)
				key := ""
				if len(vals) > 0 {
					key = jqToString(vals[0])
				}
				if !seen[key] {
					seen[key] = true
					result = append(result, item)
				}
			}
			return []interface{}{result}, nil
		}
		return []interface{}{input}, nil

	case "reverse":
		if arr, ok := input.([]interface{}); ok {
			reversed := make([]interface{}, len(arr))
			for i, v := range arr {
				reversed[len(arr)-1-i] = v
			}
			return []interface{}{reversed}, nil
		}
		if s, ok := input.(string); ok {
			runes := []rune(s)
			for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
				runes[i], runes[j] = runes[j], runes[i]
			}
			return []interface{}{string(runes)}, nil
		}
		return []interface{}{input}, nil

	case "min":
		if arr, ok := input.([]interface{}); ok && len(arr) > 0 {
			minVal := arr[0]
			for _, v := range arr[1:] {
				if jqLessThan(v, minVal) {
					minVal = v
				}
			}
			return []interface{}{minVal}, nil
		}
		return []interface{}{nil}, nil

	case "max":
		if arr, ok := input.([]interface{}); ok && len(arr) > 0 {
			maxVal := arr[0]
			for _, v := range arr[1:] {
				if jqLessThan(maxVal, v) {
					maxVal = v
				}
			}
			return []interface{}{maxVal}, nil
		}
		return []interface{}{nil}, nil

	case "min_by":
		if arr, ok := input.([]interface{}); ok && len(arr) > 0 && len(node.children) > 0 {
			minItem := arr[0]
			minVals, _ := jqEval(node.children[0], minItem)
			for _, item := range arr[1:] {
				vals, _ := jqEval(node.children[0], item)
				var mv, cv interface{}
				if len(minVals) > 0 {
					mv = minVals[0]
				}
				if len(vals) > 0 {
					cv = vals[0]
				}
				if jqLessThan(cv, mv) {
					minItem = item
					minVals = vals
				}
			}
			return []interface{}{minItem}, nil
		}
		return []interface{}{nil}, nil

	case "max_by":
		if arr, ok := input.([]interface{}); ok && len(arr) > 0 && len(node.children) > 0 {
			maxItem := arr[0]
			maxVals, _ := jqEval(node.children[0], maxItem)
			for _, item := range arr[1:] {
				vals, _ := jqEval(node.children[0], item)
				var mv, cv interface{}
				if len(maxVals) > 0 {
					mv = maxVals[0]
				}
				if len(vals) > 0 {
					cv = vals[0]
				}
				if jqLessThan(mv, cv) {
					maxItem = item
					maxVals = vals
				}
			}
			return []interface{}{maxItem}, nil
		}
		return []interface{}{nil}, nil

	case "first":
		if arr, ok := input.([]interface{}); ok && len(arr) > 0 {
			return []interface{}{arr[0]}, nil
		}
		return []interface{}{nil}, nil

	case "last":
		if arr, ok := input.([]interface{}); ok && len(arr) > 0 {
			return []interface{}{arr[len(arr)-1]}, nil
		}
		return []interface{}{nil}, nil

	case "nth":
		if len(node.children) > 0 {
			vals, _ := jqEval(node.children[0], input)
			if len(vals) > 0 {
				if arr, ok := input.([]interface{}); ok {
					idx := jqToInt(vals[0])
					if idx >= 0 && idx < len(arr) {
						return []interface{}{arr[idx]}, nil
					}
				}
			}
		}
		return []interface{}{nil}, nil

	case "to_entries":
		m, ok := input.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("cannot convert %s to entries", jqTypeName(input))
		}
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var result []interface{}
		for _, k := range keys {
			result = append(result, map[string]interface{}{"key": k, "value": m[k]})
		}
		return []interface{}{result}, nil

	case "from_entries":
		arr, ok := input.([]interface{})
		if !ok {
			return nil, fmt.Errorf("cannot convert %s from entries", jqTypeName(input))
		}
		result := make(map[string]interface{})
		for _, item := range arr {
			m, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			key := ""
			if k, ok := m["key"]; ok {
				key = jqToString(k)
			} else if k, ok := m["name"]; ok {
				key = jqToString(k)
			}
			result[key] = m["value"]
		}
		return []interface{}{result}, nil

	case "with_entries":
		if len(node.children) > 0 {
			m, ok := input.(map[string]interface{})
			if !ok {
				return nil, fmt.Errorf("cannot with_entries on %s", jqTypeName(input))
			}
			keys := make([]string, 0, len(m))
			for k := range m {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			result := make(map[string]interface{})
			for _, k := range keys {
				entry := map[string]interface{}{"key": k, "value": m[k]}
				vals, err := jqEval(node.children[0], entry)
				if err != nil {
					continue
				}
				for _, v := range vals {
					if newEntry, ok := v.(map[string]interface{}); ok {
						newKey := jqToString(newEntry["key"])
						result[newKey] = newEntry["value"]
					}
				}
			}
			return []interface{}{result}, nil
		}
		return []interface{}{input}, nil

	case "limit":
		if len(node.children) >= 2 {
			nVals, _ := jqEval(node.children[0], input)
			if len(nVals) > 0 {
				n := jqToInt(nVals[0])
				results, err := jqEval(node.children[1], input)
				if err != nil {
					return nil, err
				}
				if n < len(results) {
					return results[:n], nil
				}
				return results, nil
			}
		}
		return []interface{}{input}, nil

	case "not":
		return []interface{}{!jqIsTruthy(input)}, nil

	case "debug":
		data, _ := json.Marshal(input)
		_ = data // debug is a no-op in our shell
		return []interface{}{input}, nil

	case "env":
		return []interface{}{map[string]interface{}{}}, nil

	case "recurse":
		return jqRecurseValues(input), nil

	case "walk":
		if len(node.children) > 0 {
			result, err := jqWalk(input, node.children[0])
			if err != nil {
				return nil, err
			}
			return []interface{}{result}, nil
		}
		return []interface{}{input}, nil

	case "inside":
		if len(node.children) > 0 {
			vals, _ := jqEval(node.children[0], input)
			if len(vals) > 0 {
				return []interface{}{jqContains(vals[0], input)}, nil
			}
		}
		return []interface{}{false}, nil

	case "@base64":
		if s, ok := input.(string); ok {
			return []interface{}{base64Encode(s)}, nil
		}
		data, _ := json.Marshal(input)
		return []interface{}{base64Encode(string(data))}, nil

	case "@base64d":
		if s, ok := input.(string); ok {
			decoded, err := base64Decode(s)
			if err != nil {
				return nil, err
			}
			return []interface{}{decoded}, nil
		}
		return []interface{}{input}, nil

	case "@csv":
		if arr, ok := input.([]interface{}); ok {
			var parts []string
			for _, v := range arr {
				s := jqToString(v)
				if strings.Contains(s, ",") || strings.Contains(s, "\"") || strings.Contains(s, "\n") {
					s = "\"" + strings.ReplaceAll(s, "\"", "\"\"") + "\""
				}
				parts = append(parts, s)
			}
			return []interface{}{strings.Join(parts, ",")}, nil
		}
		return []interface{}{jqToString(input)}, nil

	case "@tsv":
		if arr, ok := input.([]interface{}); ok {
			var parts []string
			for _, v := range arr {
				parts = append(parts, jqToString(v))
			}
			return []interface{}{strings.Join(parts, "\t")}, nil
		}
		return []interface{}{jqToString(input)}, nil

	case "@json":
		data, _ := json.Marshal(input)
		return []interface{}{string(data)}, nil

	case "@text", "@html", "@uri":
		return []interface{}{jqToString(input)}, nil
	}

	return nil, fmt.Errorf("unknown function: %s", name)
}

func jqTypeName(v interface{}) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case float64:
		return "number"
	case string:
		return "string"
	case []interface{}:
		return "array"
	case map[string]interface{}:
		return "object"
	}
	return "unknown"
}

func jqLessThan(a, b interface{}) bool {
	af, aOk := a.(float64)
	bf, bOk := b.(float64)
	if aOk && bOk {
		return af < bf
	}
	return jqToString(a) < jqToString(b)
}

func jqSortArray(arr []interface{}) {
	sort.SliceStable(arr, func(i, j int) bool {
		return jqLessThan(arr[i], arr[j])
	})
}

func jqFlatten(arr []interface{}) []interface{} {
	var result []interface{}
	for _, item := range arr {
		if subArr, ok := item.([]interface{}); ok {
			result = append(result, jqFlatten(subArr)...)
		} else {
			result = append(result, item)
		}
	}
	return result
}

func jqContains(container, contained interface{}) bool {
	cj, _ := json.Marshal(container)
	dj, _ := json.Marshal(contained)
	return strings.Contains(string(cj), string(dj))
}

func jqWalk(v interface{}, expr *jqNode) (interface{}, error) {
	switch val := v.(type) {
	case map[string]interface{}:
		result := make(map[string]interface{})
		for k, v2 := range val {
			walked, err := jqWalk(v2, expr)
			if err != nil {
				return nil, err
			}
			result[k] = walked
		}
		vals, err := jqEval(expr, result)
		if err != nil {
			return nil, err
		}
		if len(vals) > 0 {
			return vals[0], nil
		}
		return result, nil
	case []interface{}:
		var result []interface{}
		for _, item := range val {
			walked, err := jqWalk(item, expr)
			if err != nil {
				return nil, err
			}
			result = append(result, walked)
		}
		vals, err := jqEval(expr, result)
		if err != nil {
			return nil, err
		}
		if len(vals) > 0 {
			return vals[0], nil
		}
		return result, nil
	default:
		vals, err := jqEval(expr, v)
		if err != nil {
			return nil, err
		}
		if len(vals) > 0 {
			return vals[0], nil
		}
		return v, nil
	}
}

func base64Encode(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}

func base64Decode(s string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
