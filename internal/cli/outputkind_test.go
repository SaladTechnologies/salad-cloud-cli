package cli

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/spf13/cobra"
)

// Every leaf command declares what its JSON output is, so a new command
// cannot skip the output rule unnoticed: a state change the API
// answers with a resource emits the resource; any other state change, the
// API's or the config file's, carries "action" and "ok"; reads emit what
// they read; logs streams; ssh and sftp hand stdout to the session.
const (
	kindRead     = "read"
	kindResource = "resource" // the API's own resource from a write
	kindAction   = "action"   // carries action and ok
	kindStream   = "stream"   // logs: NDJSON
	kindSession  = "session"  // ssh, sftp: the remote session's stdout
)

type outputKind struct {
	kind   string
	result any // for kindAction: the result type the command emits
}

var outputKinds = map[string]outputKind{
	"saladcloud agent init":                            {kindAction, agentInitResult{}},
	"saladcloud ai-gateway login":                      {kindAction, gatewayLoginResult{}},
	"saladcloud ai-gateway logout":                     {kindAction, gatewayLogoutResult{}},
	"saladcloud ai-gateway models list":                {kind: kindRead},
	"saladcloud ai-gateway test":                       {kind: kindRead},
	"saladcloud availability":                          {kind: kindRead},
	"saladcloud changelog":                             {kind: kindRead},
	"saladcloud config get":                            {kind: kindRead},
	"saladcloud config list":                           {kind: kindRead},
	"saladcloud config path":                           {kind: kindRead},
	"saladcloud config set":                            {kindAction, configSetResult{}},
	"saladcloud config unset":                          {kindAction, configUnsetResult{}},
	"saladcloud container-groups cp":                   {kindAction, cpResult{}},
	"saladcloud container-groups create":               {kind: kindResource},
	"saladcloud container-groups delete":               {kindAction, groupActionResult{}},
	"saladcloud container-groups get":                  {kind: kindRead},
	"saladcloud container-groups instances get":        {kind: kindRead},
	"saladcloud container-groups instances list":       {kind: kindRead},
	"saladcloud container-groups instances reallocate": {kindAction, instanceActionResult{}},
	"saladcloud container-groups instances recreate":   {kindAction, instanceActionResult{}},
	"saladcloud container-groups instances restart":    {kindAction, instanceActionResult{}},
	"saladcloud container-groups instances update":     {kind: kindResource},
	"saladcloud container-groups list":                 {kind: kindRead},
	"saladcloud container-groups logs":                 {kind: kindStream},
	"saladcloud container-groups scale":                {kindAction, scaleResult{}},
	"saladcloud container-groups sftp":                 {kind: kindSession},
	"saladcloud container-groups ssh":                  {kind: kindSession},
	"saladcloud container-groups start":                {kindAction, groupActionResult{}},
	"saladcloud container-groups stop":                 {kindAction, groupActionResult{}},
	"saladcloud container-groups update":               {kind: kindResource},
	"saladcloud doctor":                                {kind: kindRead},
	"saladcloud gpus get":                              {kind: kindRead},
	"saladcloud gpus list":                             {kind: kindRead},
	"saladcloud inference get":                         {kind: kindRead},
	"saladcloud inference jobs cancel":                 {kindAction, jobActionResult{}},
	"saladcloud inference jobs create":                 {kind: kindResource},
	"saladcloud inference jobs get":                    {kind: kindRead},
	"saladcloud inference jobs list":                   {kind: kindRead},
	"saladcloud inference list":                        {kind: kindRead},
	"saladcloud inference transcribe":                  {kind: kindResource},
	"saladcloud inference transcription-lite":          {kind: kindResource},
	"saladcloud login":                                 {kindAction, loginResult{}},
	"saladcloud logout":                                {kindAction, logoutResult{}},
	"saladcloud logs":                                  {kind: kindStream},
	"saladcloud orgs get":                              {kind: kindRead},
	"saladcloud orgs list":                             {kind: kindRead},
	"saladcloud queues create":                         {kind: kindResource},
	"saladcloud queues delete":                         {kindAction, queueActionResult{}},
	"saladcloud queues get":                            {kind: kindRead},
	"saladcloud queues jobs cancel":                    {kindAction, queueJobActionResult{}},
	"saladcloud queues jobs create":                    {kind: kindResource},
	"saladcloud queues jobs get":                       {kind: kindRead},
	"saladcloud queues jobs list":                      {kind: kindRead},
	"saladcloud queues list":                           {kind: kindRead},
	"saladcloud queues update":                         {kind: kindResource},
	"saladcloud quotas get":                            {kind: kindRead},
	"saladcloud ssh-key get":                           {kind: kindRead},
	"saladcloud ssh-key setup":                         {kindAction, sshKeySetupResult{}},
	"saladcloud use":                                   {kindAction, useResult{}},
	"saladcloud version":                               {kind: kindRead},
	"saladcloud webhook-secret get":                    {kind: kindRead},
	"saladcloud webhook-secret rotate":                 {kind: kindResource},
	"saladcloud whoami":                                {kind: kindRead},
}

func TestEveryLeafDeclaresItsOutputKind(t *testing.T) {
	seen := make(map[string]bool)
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if !c.HasSubCommands() && c.Runnable() && !c.Hidden && c.Name() != "help" && c.Parent() != nil &&
			c.Parent().Name() != "completion" {
			path := c.CommandPath()
			seen[path] = true
			if _, ok := outputKinds[path]; !ok {
				t.Errorf("%s declares no output kind: add it to outputKinds (an action carries action and ok)", path)
			}
		}
		for _, s := range c.Commands() {
			walk(s)
		}
	}
	walk(rootCmd)

	var stale []string
	for path := range outputKinds {
		if !seen[path] {
			stale = append(stale, path)
		}
	}
	slices.Sort(stale)
	for _, path := range stale {
		t.Errorf("outputKinds lists %q, which is not a command", path)
	}

	for path, k := range outputKinds {
		if k.kind != kindAction {
			continue
		}
		data, err := json.Marshal(k.result)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		var m map[string]json.RawMessage
		noErr(t, json.Unmarshal(data, &m))
		for _, key := range []string{"action", "ok"} {
			if _, ok := m[key]; !ok {
				t.Errorf("%s changes state but its result lacks %q", path, key)
			}
		}
	}
}
