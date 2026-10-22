# Native Squad 1.1.0 cast fixtures

These are unchanged outputs of the published [Squad v1.1.0](https://github.com/bradygaster/squad/releases/tag/v1.1.0)
Linux x64 standalone bundle, source commit
`c24da2aad2ab7576f6b68dec2bc50b2fe2dc7a8a` (not the moving dev branch).
The upstream bundle uses Node 22.20.0; no Squad code or runtime is shipped by kmx.

Verified `squad-linux-x64.tar.gz` SHA256 against the published release inventory:
`2de647a3feaef334051eb1b3084acba37a511818aad029a16abfe32fadb41aff`.
In a fresh Git repository with a private HOME/config directory, stdin closed and
no terminal, ran once:

```sh
squad init --no-workflows --no-vscode-default
squad cast --name leader --role lead
squad cast --name greeter --role 'Greeting specialist'
```

- `catalog.md`: actual `.squad/agents/leader/charter.md`, SHA256
  `e95bec785702a410617b695bb93c75b45c199fd6c3e4ffec4744edd18bef523b`.
  Uses the catalog generator. Identity has Role/Expertise/Style but **no Name**.
  Includes the native attribution comment and has **no final newline**.
- `fallback.md`: actual `.squad/agents/greeter/charter.md`, SHA256
  `12ae2130cda48db0ede019aeb190da150ba374c6c157e2dc9e997e836c07dcf6`.
  Uses cast's inline fallback, not just the template file. Identity includes Name;
  this file ends in one LF.

The fixture storage basenames are not cast identity. Parser tests supply the
approved member ID/display role and canonical native placement explicitly.
Both native files retain `Preferred: auto` and unfilled native prose placeholders.
Tests add Task Instructions/Examples in memory; no native section is rewritten.
The Role catalog and fallback paths are distinct in
[cast.ts](https://github.com/bradygaster/squad/blob/c24da2aad2ab7576f6b68dec2bc50b2fe2dc7a8a/packages/squad-cli/src/cli/commands/cast.ts).
This is Linux template qualification, not installed-host dispatch, Windows/macOS
casting, upgrade preservation or package-digest evaluation evidence.

Attribution: Squad, Brady Gaster and contributors (MIT); catalog prose adapted
from agency-agents by AgentLand Contributors (MIT). Full notices: [LICENSE.txt](LICENSE.txt).
