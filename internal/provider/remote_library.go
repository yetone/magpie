package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// RemoteLibraryPath is where a remote magpie tells its Library's prompts
// and skills (KoiLx0 on X: managed on one computer, not on every client).
// It is answered to the computer itself and to a full-access gateway key
// of a magpie shared on the local network, as the quotas are.
const RemoteLibraryPath = "/v1/magpie/library"

// RemoteLibraryWait is how long a remote magpie has to send its skills.
const RemoteLibraryWait = 90 * time.Second

// maxRemoteLibrary is the most a remote's Library answer is read to: its
// skills' files, base64 in JSON (library.Shared keeps them under 24 MiB).
const maxRemoteLibrary = 48 << 20

const errRemoteNoLibrary = "remote magpie doesn't share its Library; update magpie on that computer"

// RemoteLibrary is the body of the Library the remote magpie id shares,
// the remote's name and the answer's ETag. tag, when not "", is the ETag
// of the body read last: a remote whose Library is still that answers
// 304, and the body is nil (frankittee on Discord: read every minute, an
// unchanged Library isn't sent again). A magpie from before ETags sends
// the body each time.
func RemoteLibrary(ctx context.Context, id, tag string) (body []byte, name, etag string, err error) {
	p, err := Find(id)
	if err != nil {
		return nil, "", "", err
	}
	if !p.IsRemoteMagpie() {
		return nil, "", "", fmt.Errorf("%s is no remote magpie", id)
	}
	if p.Key == "" || p.Anthropic == "" {
		return nil, "", "", errors.New("the remote magpie's address and key are needed")
	}
	b, etag, err := remoteBodyTagged(ctx, *p, http.MethodGet, RemoteLibraryPath, RemoteLibraryWait, maxRemoteLibrary, errRemoteNoLibrary, tag)
	return b, remoteName(*p), etag, err
}
