package cli

import (
	"errors"
	"regexp"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
)

// Usage errors print what to type instead. A positional in a flag's place,
// a missing argument or required flag, a flag value of the wrong type: each
// names the input and prints a usage or corrected line, in one phrasing for
// the whole tree, replacing cobra's and pflag's defaults.

const (
	// annotationFlagForArg names the flag a stray positional most likely
	// meant: the parent flag (container-group, endpoint, queue) on a verb
	// whose subject sits inside it, or instance on ssh, sftp and logs.
	annotationFlagForArg = "saladcloud/flag-for-arg"
	// annotationGroupIsArg marks the commands that take the group as their
	// argument (ssh, sftp, container-groups logs) or inside a remote path
	// (cp); they carry a hidden -c so a -c habit gets the corrected line.
	annotationGroupIsArg  = "saladcloud/group-is-arg"
	annotationArgsWrapped = "saladcloud/args-wrapped"
)

func annotate(c *cobra.Command, key, value string) {
	if c.Annotations == nil {
		c.Annotations = make(map[string]string)
	}
	c.Annotations[key] = value
}

// addHiddenGroupFlag gives c the hidden -c that only ever produces the
// corrected command: the group is c's argument (or part of cp's path).
func addHiddenGroupFlag(c *cobra.Command) {
	c.Flags().StringP("container-group", "c", "", "")
	noteTreeErr(c.Flags().MarkHidden("container-group"))
	annotate(c, annotationGroupIsArg, "true")
}

// wrapArgs puts the usage rules in front of every leaf's own Args check:
// the hidden -c first, then the positional count, then the required flags
// (before cobra's own check, so its phrasing never shows). Idempotent.
func wrapArgs(root *cobra.Command) {
	walkCommands(root, func(c *cobra.Command) {
		if c == root || c.HasSubCommands() || c.Args == nil || c.Annotations[annotationArgsWrapped] != "" {
			return
		}
		orig := c.Args
		c.Args = func(cmd *cobra.Command, args []string) error {
			if cerr := groupGivenAsFlag(cmd, args); cerr != nil {
				return cerr
			}
			if err := orig(cmd, args); err != nil {
				var cerr *clierr.Error
				if errors.As(err, &cerr) {
					return cerr
				}
				return argsError(cmd, args, err)
			}
			//nolint:revive // if-return: returned directly, a nil *clierr.Error would be a non-nil error
			if cerr := missingRequiredFlag(cmd); cerr != nil {
				return cerr
			}
			return nil
		}
		annotate(c, annotationArgsWrapped, "true")
	})
}

func walkCommands(c *cobra.Command, fn func(*cobra.Command)) {
	fn(c)
	for _, sub := range c.Commands() {
		walkCommands(sub, fn)
	}
}

// commandName is cmd's path for messages, without the binary and without
// container-groups: "instances list", "ssh", "queues jobs create".
func commandName(cmd *cobra.Command) string {
	name := strings.TrimPrefix(cmd.CommandPath(), cmd.Root().Name()+" ")
	return strings.TrimPrefix(name, "container-groups ")
}

// usageLine is cmd's synopsis: the full path and its Use, without cobra's
// "[flags]" suffix.
func usageLine(cmd *cobra.Command) string {
	return strings.TrimSuffix(cmd.UseLine(), " [flags]")
}

// positionalPlaceholders are the argument placeholders in cmd's Use, up to
// the first flag: "restart <id> -c <group>" gives [<id>].
func positionalPlaceholders(cmd *cobra.Command) []string {
	var out []string
	for _, w := range strings.Fields(cmd.Use)[1:] {
		if strings.HasPrefix(w, "-") || strings.HasPrefix(w, "[-") || strings.HasPrefix(w, "(") {
			break
		}
		out = append(out, w)
	}
	return out
}

// flagSpelling is the spelling a message uses for f: the short form when
// there is one ("-c"), else the long form.
func flagSpelling(f *pflag.Flag) string {
	if f.Shorthand != "" {
		return "-" + f.Shorthand
	}
	return "--" + f.Name
}

// flagPlaceholder finds f's value placeholder in cmd's Use ("-c <group>"
// gives "<group>"); a few flags that are not in any Use have a fixed one.
func flagPlaceholder(cmd *cobra.Command, f *pflag.Flag) string {
	words := strings.Fields(cmd.Use)
	for i, w := range words {
		w = strings.TrimLeft(w, "[(")
		if (w == "--"+f.Name || (f.Shorthand != "" && w == "-"+f.Shorthand)) && i+1 < len(words) &&
			strings.HasPrefix(words[i+1], "<") {
			return strings.TrimRight(words[i+1], "])")
		}
	}
	switch f.Name {
	case "container-group":
		return "<group>"
	case "instance":
		return "<id>"
	}
	return "<" + f.Name + ">"
}

// flagWithPlaceholder renders a flag for a message:
// "-c/--container-group <group>", "--endpoint <endpoint>".
func flagWithPlaceholder(cmd *cobra.Command, f *pflag.Flag) string {
	name := "--" + f.Name
	if f.Shorthand != "" {
		name = "-" + f.Shorthand + "/" + name
	}
	if f.Value.Type() == "bool" {
		return name
	}
	return name + " " + flagPlaceholder(cmd, f)
}

// shellWord quotes w for a POSIX shell when it needs it.
var plainWord = regexp.MustCompile(`^[A-Za-z0-9@%+=:,./_-]+$`)

func shellWord(w string) string {
	if plainWord.MatchString(w) {
		return w
	}
	return shSingleQuote(w)
}

// changedFlagWords renders the flags set on the command line, except skip,
// as words: "-o json", "--wait", "-e A=1 -e B=2".
func changedFlagWords(cmd *cobra.Command, skip ...string) []string {
	skipped := map[string]bool{"help": true}
	for _, s := range skip {
		skipped[s] = true
	}
	var words []string
	cmd.Flags().Visit(func(f *pflag.Flag) {
		if skipped[f.Name] || f.Hidden || !f.Changed { // Visit also walks flags an earlier parse set
			return
		}
		name := flagSpelling(f)
		switch v := f.Value.(type) {
		case pflag.SliceValue:
			for _, s := range v.GetSlice() {
				words = append(words, name, shellWord(s))
			}
		default:
			if f.Value.Type() == "bool" {
				if f.Value.String() == "true" {
					words = append(words, "--"+f.Name)
				} else {
					words = append(words, "--"+f.Name+"=false")
				}
				return
			}
			words = append(words, name, shellWord(f.Value.String()))
		}
	})
	return words
}

// correctedLine is the command to type instead: cmd's full path, its
// positionals, the misplaced value as its flag, the other flags as given,
// and anything after "--".
func correctedLine(cmd *cobra.Command, positionals []string, f *pflag.Flag, value string, dash []string) string {
	words := strings.Fields(cmd.CommandPath())
	for _, p := range positionals {
		words = append(words, shellWord(p))
	}
	if f != nil {
		words = append(words, flagSpelling(f), shellWord(value))
		words = append(words, changedFlagWords(cmd, f.Name)...)
	} else {
		words = append(words, changedFlagWords(cmd)...)
	}
	if len(dash) > 0 {
		words = append(words, "--")
		for _, d := range dash {
			words = append(words, shellWord(d))
		}
	}
	return strings.Join(words, " ")
}

// splitDash separates the positionals before "--" from the words after it,
// for a command whose Use takes a command after "--" (ssh).
func splitDash(cmd *cobra.Command, args []string) (pos, dash []string) {
	if !strings.Contains(cmd.Use, "[-- <") {
		return args, nil
	}
	if n := cmd.ArgsLenAtDash(); n >= 0 && n <= len(args) {
		return args[:n], args[n:]
	}
	return args, nil
}

// cobra's positional-count errors, and the bounds each states.
var (
	noArgsRe    = regexp.MustCompile(`^unknown command ".*" for ".*"$`)
	exactArgsRe = regexp.MustCompile(`^accepts (\d+) arg\(s\)(?: before --)?, received \d+$`)
	maxArgsRe   = regexp.MustCompile(`^accepts at most (\d+) arg\(s\), received \d+$`)
	minArgsRe   = regexp.MustCompile(`^requires at least (\d+) arg\(s\), only received \d+$`)
	rangeArgsRe = regexp.MustCompile(`^accepts between (\d+) and (\d+) arg\(s\), received \d+$`)
)

// argBounds reads the positional bounds from a cobra Args error; ok is
// false for any other error.
func argBounds(err error) (minArgs, maxArgs int, ok bool) {
	msg := err.Error()
	atoi := func(s string) int {
		n, err := strconv.Atoi(s)
		if err != nil {
			return 0 // not reached: the patterns match digits only
		}
		return n
	}
	switch {
	case noArgsRe.MatchString(msg):
		return 0, 0, true
	case exactArgsRe.MatchString(msg):
		n := atoi(exactArgsRe.FindStringSubmatch(msg)[1])
		return n, n, true
	case maxArgsRe.MatchString(msg):
		return 0, atoi(maxArgsRe.FindStringSubmatch(msg)[1]), true
	case minArgsRe.MatchString(msg):
		return atoi(minArgsRe.FindStringSubmatch(msg)[1]), int(^uint(0) >> 1), true
	case rangeArgsRe.MatchString(msg):
		m := rangeArgsRe.FindStringSubmatch(msg)
		return atoi(m[1]), atoi(m[2]), true
	}
	return 0, 0, false
}

// argsError rewrites a positional-count error: a missing argument names its
// placeholder; one argument too many, where a flag would have taken it,
// prints the corrected command; anything else names the stray argument.
func argsError(cmd *cobra.Command, args []string, err error) *clierr.Error {
	minArgs, maxArgs, ok := argBounds(err)
	if !ok {
		return clierr.New(clierr.CodeUsage, clierr.KindUsage, "%v", err)
	}
	pos, dash := splitDash(cmd, args)
	if len(pos) < minArgs {
		ph := "<argument>"
		if names := positionalPlaceholders(cmd); len(pos) < len(names) {
			ph = names[len(pos)]
		}
		return clierr.New(clierr.CodeUsage, clierr.KindUsage, "missing argument: %s", ph).
			WithHint("run %s", usageLine(cmd))
	}
	if name := cmd.Annotations[annotationFlagForArg]; name != "" && len(pos) == maxArgs+1 &&
		!cmd.Flags().Changed(name) {
		if f := cmd.Flags().Lookup(name); f != nil {
			// A parent comes first (the old <parent> <leaf> order); the
			// instance comes after the group it belongs to.
			value, rest := pos[0], pos[1:]
			if name == "instance" {
				value, rest = pos[maxArgs], pos[:maxArgs]
			}
			return clierr.New(
				clierr.CodeUsage,
				clierr.KindUsage,
				"%s takes the %s as %s, not as an argument",
				commandName(
					cmd,
				),
				flagNoun(name),
				flagSpelling(f),
			).WithHint("run %s", correctedLine(cmd, rest, f, value, dash))
		}
	}
	extra := ""
	if len(pos) > maxArgs {
		extra = pos[maxArgs]
	}
	return clierr.New(clierr.CodeUsage, clierr.KindUsage, `unexpected argument "%s"`, extra).
		WithHint("run %s", usageLine(cmd))
}

// flagNoun is what a parent flag names, for "takes the group as -c".
func flagNoun(flag string) string {
	if flag == "container-group" {
		return "group"
	}
	return flag
}

// groupGivenAsFlag answers the hidden -c on a command whose group is its
// argument: ssh, sftp and container-groups logs take the group as the
// argument and the instance as --instance; cp names both in its path.
func groupGivenAsFlag(cmd *cobra.Command, args []string) *clierr.Error {
	fr := readFlags(cmd.Flags())
	if cmd.Annotations[annotationGroupIsArg] == "" || !cmd.Flags().Changed("container-group") {
		return nil
	}
	group := fr.String("container-group")
	if cerr := fr.Err(); cerr != nil {
		return cerr
	}
	if cmd.Name() == "cp" {
		return clierr.New(clierr.CodeUsage, clierr.KindUsage, "cp takes the group in its remote path, not as -c").
			WithHint("run %s", strings.Join(strings.Fields(cmd.CommandPath()), " ")+" "+shellWord(group)+"[/<instance>]:<path> <local-path>")
	}
	pos, dash := splitDash(cmd, args)
	inst := cmd.Flags().Lookup("instance")
	value := ""
	if len(pos) > 0 && !inst.Changed {
		value = pos[0]
	} else if inst.Changed {
		value = inst.Value.String()
	}
	words := []string{shellWord(group)}
	if value != "" {
		words = append(words, "--instance", shellWord(value))
	}
	line := strings.Join(append(strings.Fields(cmd.CommandPath()), words...), " ")
	if rest := changedFlagWords(cmd, "container-group", "instance"); len(rest) > 0 {
		line += " " + strings.Join(rest, " ")
	}
	if len(dash) > 0 {
		quoted := make([]string, len(dash))
		for i, d := range dash {
			quoted[i] = shellWord(d)
		}
		line += " -- " + strings.Join(quoted, " ")
	}
	return clierr.New(clierr.CodeUsage, clierr.KindUsage,
		"%s takes the group as its argument and the instance as --instance", commandName(cmd)).WithHint("run %s", line)
}

// missingRequiredFlag is cobra's required-flag check in the CLI's phrasing,
// run before cobra's own.
func missingRequiredFlag(cmd *cobra.Command) *clierr.Error {
	var missing *pflag.Flag
	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		if missing != nil || f.Changed {
			return
		}
		if req := f.Annotations[cobra.BashCompOneRequiredFlag]; len(req) > 0 && req[0] == "true" {
			missing = f
		}
	})
	if missing == nil {
		return nil
	}
	return clierr.New(clierr.CodeUsage, clierr.KindUsage, "missing required flag: %s",
		flagWithPlaceholder(cmd, missing)).WithHint("run %s", usageLine(cmd))
}

// pflag's parse errors.
var (
	invalidValueRe = regexp.MustCompile(`^invalid argument "(.*)" for "(?:-[a-zA-Z], )?(--[^"]+)" flag: (.*)$`)
	needsValueRe   = regexp.MustCompile(`^flag needs an argument: (?:'[a-zA-Z]' in -([a-zA-Z])|--(\S+))$`)
)

// flagError rewrites pflag's parse errors in the CLI's phrasing: a value of
// the wrong type says what it should be, a flag missing its value names its
// placeholder, and a wait flag on the verb with the other default says
// which default applies.
func flagError(cmd *cobra.Command, err error) error {
	msg := err.Error()
	if m := invalidValueRe.FindStringSubmatch(msg); m != nil {
		return clierr.New(clierr.CodeUsage, clierr.KindUsage, `invalid %s "%s": %s`, m[2], m[1], valueProblem(m[3]))
	}
	if m := needsValueRe.FindStringSubmatch(msg); m != nil {
		var f *pflag.Flag
		if m[1] != "" {
			f = cmd.Flags().ShorthandLookup(m[1])
		} else {
			f = cmd.Flags().Lookup(m[2])
		}
		if f != nil {
			return clierr.New(clierr.CodeUsage, clierr.KindUsage, "missing value for %s",
				flagWithPlaceholder(cmd, f)).WithHint("run %s", usageLine(cmd))
		}
	}
	switch {
	case msg == "unknown flag: --no-wait" && cmd.Flags().Lookup("wait") != nil:
		return clierr.New(clierr.CodeUsage, clierr.KindUsage, "unknown flag: --no-wait").
			WithHint("leave it out; %s returns at once, and --wait blocks until %s", commandName(cmd), waitUntil(cmd))
	case msg == "unknown flag: --wait" && cmd.Flags().Lookup("no-wait") != nil:
		waits := "waits for the change to settle"
		if cmd.Parent() != nil && cmd.Parent().Name() == "inference" {
			waits = "waits for the transcript"
		}
		return clierr.New(clierr.CodeUsage, clierr.KindUsage, "unknown flag: --wait").
			WithHint("leave it out; %s %s by default, and --no-wait returns at once", commandName(cmd), waits)
	}
	if m := unknownFlagRe.FindStringSubmatch(msg); m != nil {
		given, near := "-"+m[1], ""
		if m[2] != "" {
			given = "--" + m[2]
			near = nearestFlag(cmd, m[2])
		}
		if near != "" {
			return clierr.New(clierr.CodeUsage, clierr.KindUsage, "unknown flag: %s", given).
				WithHint("did you mean --%s?; run %s, or to list its flags, run %s --help", near, usageLine(cmd), cmd.CommandPath())
		}
		return clierr.New(clierr.CodeUsage, clierr.KindUsage, "unknown flag: %s", given).
			WithHint("run %s, or to list its flags, run %s --help", usageLine(cmd), cmd.CommandPath())
	}
	return clierr.New(clierr.CodeUsage, clierr.KindUsage, "%v", err)
}

// unknownFlagRe matches pflag's unknown-flag errors, long and short.
var unknownFlagRe = regexp.MustCompile(`^unknown (?:shorthand )?flag: (?:'([a-zA-Z0-9])' in -\S+|--(\S+))$`)

// nearestFlag is the visible flag of cmd closest to name: within two edits
// (one for names of four characters or fewer), and only when no other flag
// is as close.
func nearestFlag(cmd *cobra.Command, name string) string {
	limit := 2
	if len(name) <= 4 {
		limit = 1
	}
	best, bestDist, tie := "", limit+1, false
	visit := func(f *pflag.Flag) {
		if f.Hidden {
			return
		}
		switch d := editDistance(name, f.Name); {
		case d < bestDist:
			best, bestDist, tie = f.Name, d, false
		case d == bestDist:
			tie = true
		}
	}
	cmd.Flags().VisitAll(visit)
	cmd.InheritedFlags().VisitAll(visit)
	if tie || bestDist > limit {
		return ""
	}
	return best
}

// waitUntil is what a verb's --wait waits for.
func waitUntil(cmd *cobra.Command) string {
	switch {
	case cmd.Parent() != nil && cmd.Parent().Name() == "jobs":
		return "the job finishes"
	case cmd.Name() == "stop":
		return "the group is stopped"
	}
	return "the group is running"
}

// valueProblem turns a strconv or time parse error into what the value
// should have been.
func valueProblem(parseErr string) string {
	switch {
	case strings.Contains(parseErr, "value out of range"):
		return "out of range"
	case strings.Contains(parseErr, "ParseInt"), strings.Contains(parseErr, "ParseUint"),
		strings.Contains(parseErr, "ParseFloat"):
		return "not a number"
	case strings.Contains(parseErr, "ParseBool"):
		return "not true or false"
	case strings.HasPrefix(parseErr, "time: "):
		return "not a duration such as 30s, 5m or 1h"
	}
	return parseErr
}
