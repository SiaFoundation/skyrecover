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
./bin/metabuild ~/testdir-base ~/testdir-extended ~/results
```
