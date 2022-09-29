# skyrecover
Attempts to rebuild skynet subfiles from downloaded metadata.

# Building

```
go build -o bin/ ./cmd/metabuild
```

# Usage
```
# download the base sector and extended metadata from skyc
skyc renter download /var/skynet/testdir ~/testdir-base
skyc renter download /var/skynet/testdir-extended ~/testdir-extended
# reconstruct the data. outputs each subfile to ~/results
./bin/metabuild --skynetdir ~/.skynet --skylink AABl3BTAQL0hoUQW942X1kNBQRDUdBIX-FixOdGz3oNHeA --base ~/testdir-base --extended ~/testdir-extended --output ~/results
```
