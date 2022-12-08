# Skynet Recovery Utils
Utilties to recover lost skyd data

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

## skyscan
Scans a base sector or extended file for a file matching a file size and checksum.

### Building
```
go build -o bin/ ./cmd/skyscan
```

### Usage
```
skyscan --algo sha256 --checksum e9cd47a43126020d93981a859eef38950eaf5d13559132d97d4c5f3281d2a251 --len 342518 --input ~/Downloads/image_download --output ~/Downloads/output.png
```

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

## healthcheck
Checks the health of a sia file exported using the `extractmeta` utility. Health check needs contracts to function, use
the sub-commands to send Siacoins and form contracts.

### Building
```
go build -o bin/ ./cmd/healthcheck
```

### Get wallet address
```
RECOVERY_PHRASE="board learn true grain combine pole talent country soon stock juice client" healthcheck -d ~/recovery-data wallet
```

### Redistribute UTXOs
```
RECOVERY_PHRASE="board learn true grain combine pole talent country soon stock juice client" healthcheck -d ~/recovery-data wallet redistribute 10 100SC
```

### Form contracts
The utility will attempt to form small download-only contracts with every working host on the network. This will take a while.
```
RECOVERY_PHRASE="board learn true grain combine pole talent country soon stock juice client" healthcheck -d ~/recovery-data contracts form
```

### Check health
```
healthcheck -d ~/recovery-data check ~/photos.sia.json
```