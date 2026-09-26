# Native image attachment attribution

AstraBridge 0.4.0 includes the native image attachment implementation from:
https://github.com/ranxi2001/sub2api/tree/26b324b44c80929e5f86aeb36e09996423c4a5c0/backend/internal/service/basispoints

attachments.go, attachments_test.go, images.go and images_test.go are unchanged.
image_attachment_support.go extracts shared constants, codec registration and
relayImagePayload from image_relay.go. internal/gateway/images.go adapts the
host HTTP integration from openai_excel_bps_attachments.go, without importing
the platform database, public image relay or account scheduler.

Sub2API license and the original protocol attribution are retained alongside
this notice. golang.org/x/image v0.25.0 supplies WebP decoding under its BSD
license. Test-only dependencies and their licenses are retained in vendor.
See upstream-manifest.json and docs/source-provenance.md for exact provenance.
