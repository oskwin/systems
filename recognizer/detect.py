#!/usr/bin/env python3
"""
detect.py  —  YOLOv8 object detection helper for the Recognizer system.

Usage:
    python3 detect.py <input_image> <output_image> [model]

Arguments:
    input_image   Path to the source JPEG file.
    output_image  Path where the annotated JPEG will be saved.
    model         YOLO model file or name (default: yolov8n.pt).

Output:
    Prints one detected class label per line to stdout.
    The annotated image (with bounding boxes) is written to output_image.

Exit codes:
    0  success
    1  argument error or detection failure
"""

import sys
import os

def main():
    if len(sys.argv) < 3:
        print(f"usage: {sys.argv[0]} <input> <output> [model]", file=sys.stderr)
        sys.exit(1)

    input_path  = sys.argv[1]
    output_path = sys.argv[2]
    model_name  = sys.argv[3] if len(sys.argv) > 3 else "yolov8n.pt"

    try:
        from ultralytics import YOLO
    except ImportError:
        print("ultralytics not installed — run: pip install ultralytics", file=sys.stderr)
        sys.exit(1)

    model = YOLO(model_name)
    results = model(input_path)

    if not results:
        sys.exit(0)

    result = results[0]

    # Save annotated image (bounding boxes drawn by ultralytics).
    os.makedirs(os.path.dirname(output_path) or ".", exist_ok=True)
    annotated = result.plot()  # numpy array, BGR

    import cv2
    cv2.imwrite(output_path, annotated)

    # Print detected class names (one per line) for the Go caller.
    names = model.names
    for cls_id in result.boxes.cls.tolist():
        print(names[int(cls_id)])

if __name__ == "__main__":
    main()
