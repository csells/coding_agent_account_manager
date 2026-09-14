package cmd

import "fmt"

// loginCommand is a provider's own login: the binary a user would run, its
// arguments, and the hint that tells them what the command will want.
type loginCommand struct {
	Bin  string
	Args []string
	Hint string
}

// loginCommandFor is the one table of provider login commands, shared by
// `caam add` and the dashboard's n key so both log the same agents in the
// same way. deviceCode picks Codex's headless device-code flow.
func loginCommandFor(provider string, deviceCode bool) (loginCommand, error) {
	switch provider {
	case "codex":
		args := []string{"login"}
		if deviceCode {
			args = append(args, "--device-auth")
		}
		return loginCommand{"codex", args, "Complete the Codex login in the browser, then come back here."}, nil
	case "claude":
		return loginCommand{"claude", nil, "Claude Code is starting: type /login, sign in, then exit to come back here."}, nil
	case "gemini":
		return loginCommand{"gemini", nil, "Gemini CLI is starting: choose 'Login with Google', then exit to come back here."}, nil
	case "agy":
		return loginCommand{"agy", nil, "Antigravity is starting: complete the Google login, then exit to come back here."}, nil
	case "kimi":
		return loginCommand{"kimi", []string{"login"}, "Complete the Kimi Code device-code login, then come back here."}, nil
	case "zcode":
		return loginCommand{"zcode", []string{"login"}, "Complete the Z.AI login, then come back here."}, nil
	case "opencode":
		return loginCommand{"opencode", []string{"auth", "login"}, "Complete the OpenCode login, then come back here."}, nil
	case "grok":
		return loginCommand{"grok", []string{"login"}, "Complete the Grok login in the browser, then come back here."}, nil
	case "cursor":
		return loginCommand{"cursor", nil, "Cursor is starting: complete the login, then exit to come back here."}, nil
	default:
		return loginCommand{}, fmt.Errorf("no login flow for %s", provider)
	}
}
