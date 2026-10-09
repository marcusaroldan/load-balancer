# load-balancer

## Containers

### Build Image:
`docker build -t lb:dev .`

### Run Image:
`docker run lb:dev -p 8080:8080`

### Image Size Comparison

| Image Type |Size (MB) |
| :---: | :---: |
| One-Stage Build (lb:naive) | 98MB |
| Multi-Stage Build (lb:dev) | 15MB |

### Notes about current Dockerfile
- Currently not pinned to an exact golang digest, need to pin exactly in the future for real builds