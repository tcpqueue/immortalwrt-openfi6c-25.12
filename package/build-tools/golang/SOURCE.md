Go 1.27.1 build recipe adapted from feeds/packages/lang/golang at 84bd86384928955b568988ca0e09e2c78c75173d.
Common multi-version build helpers are copied unchanged. Only the 1.27 compiler recipe is registered here; standard Go 1.26 and bootstrap remain provided by the standard packages feed.
Source: https://go.dev/dl/go1.27.1.src.tar.gz
SHA256: 4e408abae126d916b6164627193f2c54f0e3ca1312d693b86db45f862ab238b1
Xray 26.9.9 and OpenList 4.2.6 explicitly use golang1.27/host.
