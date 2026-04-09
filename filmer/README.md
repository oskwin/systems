# mbaigo System: Filmer

The Filmer system exposes a Raspberry Pi camera as a live video streaming service. A consumer sends a GET request to the `start` service and receives the stream URL in response. It can then open that URL to receive a continuous MJPEG stream produced by `rpicam-vid`, delivered as a multipart HTTP response.

---

## How it works

```mermaid
sequenceDiagram
    participant C as Consumer
    participant F as Filmer
    participant CAM as rpicam-vid

    C->>F: GET /filmer/PiCam/start
    F-->>C: stream URL (http://host:port/filmer/PiCam/stream)

    C->>F: GET /filmer/PiCam/stream
    F->>CAM: exec rpicam-vid -t 0 --codec mjpeg -o -
    CAM-->>F: MJPEG frames on stdout (continuous)
    loop for each JPEG frame
        F-->>C: multipart/x-mixed-replace frame
    end
    note over C,F: stream ends when the client disconnects;<br/>rpicam-vid process is killed automatically
```

---

## Services

| Service | Path | Method | Response | Description |
|---|---|---|---|---|
| `stream` | `/filmer/<asset>/start` | GET | plain text URL | Returns the URL of the live MJPEG stream |
| *(stream endpoint)* | `/filmer/<asset>/stream` | GET | `multipart/x-mixed-replace` | Live MJPEG video at 640×480, 15 fps |

The two-step design (get URL, then open stream) lets the consumer hand the stream URL off to a browser or video player without going through the Arrowhead orchestration layer for every frame.

---

## Hardware requirements

- Raspberry Pi (any model with a CSI camera connector)
- Raspberry Pi Camera Module (V1 IMX219, V2 IMX219, HQ IMX477, or Camera Module 3 IMX708)
- Raspberry Pi OS **Bookworm** or later (uses `rpicam-vid` from the libcamera stack)

> **One process at a time.** libcamera allows only one process to access the camera simultaneously. Running `rpicam-vid` or `rpicam-still` from the terminal while the Filmer service is streaming will fail with `no cameras available`.

---

## Verifying the camera

```bash
# List cameras detected by libcamera
rpicam-hello --list-cameras

# Test a short video capture to a file
rpicam-vid -t 3000 --codec mjpeg -o test.mjpeg
```

> **Note:** `vcgencmd get_camera` always reports `supported=0 detected=0` on Bookworm — this queries the legacy firmware interface which is permanently disabled. It does not reflect libcamera's view of the camera.

If `rpicam-vid` reports `no cameras available`, check:
1. The ribbon cable is firmly seated at both ends (contacts face the board on the Pi side).
2. `camera_auto_detect=1` is present in `/boot/firmware/config.txt`.
3. No other process is currently holding the camera open (`ps aux | grep rpicam`).

---

## Configuration

Edit `systemconfig.json` to match your setup:

| Field | Description |
|---|---|
| `ipAddresses` | IP addresses of the Raspberry Pi |
| `protocolsNports` → `http` | Port the system listens on (default: 20162) |
| `unit_assets[0].name` | Asset name, also used in the service URL |
| `unit_assets[0].details` → `Model` | Camera module model for documentation |
| `coreSystems` | URLs of the Service Registrar, Orchestrator, CA, and maitreD |

---

## Compiling

Build for the current machine:

```bash
go build -o filmer
```

Cross-compile for Raspberry Pi 4/5 (64-bit):

```bash
GOOS=linux GOARCH=arm64 go build -o filmer_rpi64
```

Copy to the Raspberry Pi:

```bash
scp filmer_rpi64 username@192.168.1.x:rpiExec/filmer/
```

Run from the system's own directory — it reads `systemconfig.json` from the working directory. On first run without a config file, it generates one and exits so you can fill in the correct values.

```bash
cd ~/rpiExec/filmer
./filmer_rpi64
```
