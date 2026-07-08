package sim

// parseSwayCommand splits a sway command string into its canonical parts:
// scope selector (in [brackets], may be empty), verb, and args.
//
//	"[con_id=20] split none"              → ("[con_id=20]", "split", ["none"])
//	"resize set width 50 ppt"             → ("", "resize", ["set", "width", "50", "ppt"])
//	"[con_id=20] move window to mark foo" → ("[con_id=20]", "move", ["window", "to", "mark", "foo"])
//
// Pragmatic tokenizer, not a sway-spec-compliant parser.
func parseSwayCommand(cmd string) (scope, verb string, args []string) {
	i := 0
	for i < len(cmd) && cmd[i] == ' ' {
		i++
	}
	if i < len(cmd) && cmd[i] == '[' {
		depth := 0
		start := i
		for i < len(cmd) {
			c := cmd[i]
			if c == '[' {
				depth++
			} else if c == ']' {
				depth--
				if depth == 0 {
					i++
					break
				}
			}
			i++
		}
		scope = cmd[start:i]
	}
	for i < len(cmd) && cmd[i] == ' ' {
		i++
	}

	rest := cmd[i:]
	var toks []string
	tok := ""
	for j := 0; j < len(rest); j++ {
		c := rest[j]
		if c == ' ' || c == '\t' {
			if tok != "" {
				toks = append(toks, tok)
				tok = ""
			}
			continue
		}
		tok += string(c)
	}
	if tok != "" {
		toks = append(toks, tok)
	}
	if len(toks) == 0 {
		return scope, "", nil
	}
	return scope, toks[0], toks[1:]
}
