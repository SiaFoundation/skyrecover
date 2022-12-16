# Skynet Recovery Utils
Utilties to recover lost skyd data

## extractmeta
Extracts metadata from a .sia file

### Building
```
go build -o bin/ ./cmd/extractmeta
```

### Usage
```
extractmeta ~/image_download.sia
```

## metabuild
Reconstructs Skyfiles from a local base sector and -extended file 

### Building
```
go build -o bin/ ./cmd/metabuild
```

### Usage
```
# download the base sector and extended file from skyc
skyc renter download /var/skynet/testdir ~/testdir-base
skyc renter download /var/skynet/testdir-extended ~/testdir-extended
# reconstruct the data. outputs each subfile to ~/results
metabuild --skynetdir ~/.skynet --skylink AABl3BTAQL0hoUQW942X1kNBQRDUdBIX-FixOdGz3oNHeA --base ~/testdir-base --extended ~/testdir-extended --output ~/results
```

## skyrecover
Checks the health or attempts to recover a `.sia` file from `skyd`. Requires
contracts to function, use the sub-commands to send Siacoins and form contracts.

To recover a file, skyrecover will first attempt to recover shards from the
host's listed in the `.sia` file. If that fails, it will check each contracted
host for the sectors. For the secondary scan, many contracts are required; there
are about 500 active hosts on the network. If `skyd` had a lot of churn, some
sectors may still be recoverable.

### Building
```
go build -o bin/ ./cmd/skyrecover
```

### Get wallet address
```
RECOVERY_PHRASE="board learn true grain combine pole talent country soon stock juice client" skyrecover -d ~/recovery-data wallet
```

### Redistribute UTXOs
```
RECOVERY_PHRASE="board learn true grain combine pole talent country soon stock juice client" skyrecover -d ~/recovery-data wallet redistribute 10 100SC
```

### Form contracts
The utility will attempt to form small download-only contracts with every working host on the network. This will take a while.
```
RECOVERY_PHRASE="board learn true grain combine pole talent country soon stock juice client" skyrecover -d ~/recovery-data contracts form
```

### Check health
```
skyrecover -d ~/recovery-data file check ~/photos.jpeg.sia
```

### Recover a file
```
skyrecover -d ~/recovery-data file recover -i ~/photos.jpeg.sia -o ~/photos.jpeg
```

## skyscan
Scans a downloaded file for a sub-file matching a size and checksum.

### Building
```
go build -o bin/ ./cmd/skyscan
```

### Usage
```
skyscan --algo sha256 --checksum e9cd47a43126020d93981a859eef38950eaf5d13559132d97d4c5f3281d2a251 --len 342518 --input ~/Downloads/image_download --output ~/Downloads/output.png
```