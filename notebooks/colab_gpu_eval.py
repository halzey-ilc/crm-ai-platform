# CRM AI Platform GPU evaluation on Google Colab (T4). Synthetic data only.
# Same steps as notebooks/kaggle_gpu_eval.ipynb; report.json is offered as a
# browser download at the end.
import hashlib, json, os, subprocess, time, urllib.request
from pathlib import Path

BASE = Path("/content/crmai")
WORK = BASE / "bundle"


def log(msg):
    print(time.strftime("%H:%M:%S ") + msg, flush=True)


def run(cmd, logfile):
    with open(logfile, "w") as f:
        rc = subprocess.run(cmd, stdout=f, stderr=subprocess.STDOUT).returncode
    if rc != 0:
        log("FAILED: " + " ".join(map(str, cmd)) + "\n" + Path(logfile).read_text()[-3000:])
        raise SystemExit(1)


# 1. GPU
gpu = subprocess.run(["nvidia-smi", "--query-gpu=name,memory.total", "--format=csv,noheader"],
                     capture_output=True, text=True).stdout.strip()
log("GPU: " + (gpu or "NONE"))
if not gpu:
    raise SystemExit("no GPU: Runtime -> Change runtime type -> T4 GPU")

# 2. Bundle integrity
os.chmod(WORK / "evalrun", 0o755)
manifest = (WORK / "MANIFEST.txt").read_text()
for line in manifest.splitlines():
    parts = line.split()
    if len(parts) == 2 and len(parts[0]) == 64:
        assert hashlib.sha256((WORK / parts[1]).read_bytes()).hexdigest() == parts[0], parts[1]
log("bundle ok: " + " ".join(manifest.splitlines()[:2]))

# 3. llama.cpp b11960 with CUDA (75 = T4)
LLAMA = BASE / "llama.cpp"
SERVER = LLAMA / "build/bin/llama-server"
if not SERVER.exists():
    t = time.time()
    if not LLAMA.exists():
        run(["git", "clone", "--depth", "1", "--branch", "b11960", "https://github.com/ggml-org/llama.cpp", str(LLAMA)], BASE / "clone.log")
    head = subprocess.run(["git", "-C", str(LLAMA), "rev-parse", "HEAD"], capture_output=True, text=True).stdout.strip()
    assert head.startswith("8642541b2"), head
    log("building llama.cpp " + head[:9])
    run(["cmake", "-S", str(LLAMA), "-B", str(LLAMA / "build"), "-DGGML_CUDA=ON", "-DCMAKE_CUDA_ARCHITECTURES=75",
         "-DLLAMA_CURL=OFF", "-DCMAKE_BUILD_TYPE=Release"], BASE / "cmake.log")
    run(["cmake", "--build", str(LLAMA / "build"), "--target", "llama-server", "-j", str(os.cpu_count() or 2)], BASE / "build.log")
    log("llama.cpp built in %.0f s" % (time.time() - t))

# 4. Model + sha256
from huggingface_hub import hf_hub_download
SHA256 = "13c16f426047e2de38cd075bdade4a7bcbc8c774384876f677740cda65f8a983"
MODEL = Path(hf_hub_download("bartowski/Qwen_Qwen3.5-4B-GGUF", "Qwen_Qwen3.5-4B-Q4_K_M.gguf", local_dir=str(BASE / "models")))
h = hashlib.sha256()
with open(MODEL, "rb") as f:
    for chunk in iter(lambda: f.read(1 << 24), b""):
        h.update(chunk)
assert h.hexdigest() == SHA256, "model sha256 mismatch"
log("model ok: %d MB" % (MODEL.stat().st_size // 2**20))

# 5. llama-server on GPU
srv_log = open(BASE / "llama-server.log", "w")
server = subprocess.Popen([str(SERVER), "-m", str(MODEL), "--host", "127.0.0.1", "--port", "8181", "--ctx-size", "8192",
                           "--parallel", "1", "-ngl", "99", "--jinja", "--no-webui"], stdout=srv_log, stderr=subprocess.STDOUT)
for _ in range(150):
    try:
        if urllib.request.urlopen("http://127.0.0.1:8181/health", timeout=2).status == 200:
            break
    except Exception:
        time.sleep(2)
else:
    log("server did not start:\n" + (BASE / "llama-server.log").read_text()[-3000:])
    raise SystemExit(1)
offload = subprocess.run("grep -iE 'offloaded|CUDA0' %s | tail -3" % (BASE / "llama-server.log"), shell=True,
                         capture_output=True, text=True).stdout
log("llama-server ready\n" + offload)

# 6. Evaluation
gpu_name = gpu.split(",")[0].strip()
datasets = sorted(str(p) for p in (WORK / "datasets").glob("*.jsonl"))
cmd = [str(WORK / "evalrun"), "-provider", "llamacpp", "-llm", "http://127.0.0.1:8181",
       "-model", "qwen3.5-4b-q4km@" + gpu_name, "-out", str(BASE / "report.json"), *datasets]
proc = subprocess.Popen(cmd, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
for line in proc.stdout:
    log(line.rstrip())
rc = proc.wait()
server.terminate()
log("evalrun exit %d" % rc)
if (BASE / "report.json").exists():
    from google.colab import files
    files.download(str(BASE / "report.json"))
log("DONE")
