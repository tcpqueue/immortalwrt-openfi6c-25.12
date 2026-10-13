Go 1.27.2 build recipe adapted from feeds/packages/lang/golang at 84bd86384928955b568988ca0e09e2c78c75173d.
Common multi-version build helpers are copied unchanged. Only the 1.27 compiler recipe is registered here; standard Go 1.26 and bootstrap remain provided by the standard packages feed.
Source: https://go.dev/dl/go1.27.2.src.tar.gz
SHA256: 03495da2ba64894d40f5c4992e49454fa78b50690604ff92b6afff5081b76e62
Xray 26.10.10 and OpenList 4.2.6 explicitly use golang1.27/host.
