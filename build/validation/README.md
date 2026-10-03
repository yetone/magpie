# Balance template validation

From the repository on Windows, run:

```powershell
./build/validation/validate.ps1
```

The script starts Docker Desktop if needed, uses the `desktop-linux` context,
builds from the current workspace (including uncommitted changes), runs
`make test` and Chromium/WebKit regressions, and exercises the compiled Linux
application's real Web API against local fixture vendors. No Magpie API is
mocked in `balance-e2e.test.cjs`; fixtures replace only the upstream vendors.

The dedicated Dockerfile includes `build/windows` resources, generates the
Windows icon and manifest with the existing release target, and exports the
Windows amd64 desktop executable. Dependencies and generated resources stay
inside Docker. The existing production Dockerfile is unchanged.

Results are under `dist/balance-validation/`: the EXE, SHA-256, source manifest,
build/test logs, screenshots, actual vendor request records, and verification
report. A successful build alone does not mark verification as passed.
The Windows native window is not tested by this Linux-container workflow.

After all tests pass, the script leaves an isolated preview container running
on `127.0.0.1:13430`; the exact authenticated link is printed and saved in the
report. The New API demo deliberately lacks an account ID: fill `42` to see
`$3.00`. Sub2API reports `$12.50`. Both use fake tokens and a demo API key.
Configuration is kept in the preview container, not your Magpie profile.

```powershell
./build/validation/validate.ps1 -WebPort 13431
./build/validation/validate.ps1 -Action Stop
```

Stop removes only containers labelled as owned by this workspace and keeps
Docker Desktop, images, build caches and unrelated containers. An occupied port
or a name owned by another workspace is an error. No commit, registry push,
or pull request is performed.
