# ACP proxy boundaries

Use one flat provider command configuration and the coder ACP Go SDK.
The inspected `go-adk-acpagent` low-level client shares its package with ADK
wrappers, so importing it brings orchestration dependencies the proxy does not
use. Its process ownership and permission handling inform this implementation.

ACP v1 and text Responses share provider and session routing. Responses uses
explicit `store:false` and client-managed text history replay. Providers retain
separate histories in ACP sessions. Client filesystem and terminal APIs are
unadvertised; permissions are forwarded only to an ACP client.

Each provider declares an explicit command string and common environment/session
settings. Provider IDs only identify routes; no type discriminator or
provider-specific command resolver is needed.
acpboss does not define, resolve or merge MCP servers; ACP session parameters
from the client pass through to backend providers.

Tokenize command strings with shell-style quoting and escaping, then launch
the executable directly. Shell evaluation is not part of provider configuration.
