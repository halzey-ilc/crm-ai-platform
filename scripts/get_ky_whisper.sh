#!/usr/bin/env bash
# Fetch a Whisper model fine-tuned on Kyrgyz from Hugging Face and convert it to the
# ggml format whisper.cpp serves (whisper-ky in compose.gpu.yaml). Runs the
# conversion in a throwaway Python container; nothing is installed on the host.
#
#   AI_KY_WHISPER_REPO=the-cramer-project/AkylAI-STT-small scripts/get_ky_whisper.sh
#
# Candidates (check each licence before production use):
#   the-cramer-project/AkylAI-STT-small  Whisper-small, Apache-2.0 (the default)
#   UlutSoftLLC/whisper-small-kyrgyz     Whisper-small
#   BuzzASR/kyrgyz                       Whisper-large-v3 base; its card reports
#                                        CER 11% vs 35% zero-shot (not verified here)
# Compare them on your own recordings (docs/GPU_DEPLOY.md, "Kyrgyz speech").
set -euo pipefail
REPO=${AI_KY_WHISPER_REPO:-the-cramer-project/AkylAI-STT-small}
DIR=${AI_MODELS_DIR:-$HOME/.local/share/crm-ai-platform/models}
OUT=${AI_WHISPER_KY_MODEL_FILE:-ggml-kyrgyz.bin}
mkdir -p "$DIR"

if [ -f "$DIR/$OUT" ]; then
  echo "present: $DIR/$OUT (delete it to convert again)"
  exit 0
fi

docker run --rm -v "$DIR":/models -e REPO="$REPO" -e OUT="$OUT" python:3.12-slim-bookworm bash -euo pipefail -c '
  apt-get update -qq && apt-get install -y -qq git >/dev/null
  pip install -q --no-cache-dir --index-url https://download.pytorch.org/whl/cpu torch
  pip install -q --no-cache-dir transformers huggingface_hub numpy
  cd /tmp
  git clone -q --depth 1 https://github.com/ggml-org/whisper.cpp
  git clone -q --depth 1 https://github.com/openai/whisper
  python -I -c "from huggingface_hub import snapshot_download; snapshot_download(\"$REPO\", local_dir=\"/tmp/hf\")"
  python whisper.cpp/models/convert-h5-to-ggml.py /tmp/hf /tmp/whisper /tmp
  mv /tmp/ggml-model.bin "/models/$OUT"
'
echo "converted $REPO -> $DIR/$OUT"
