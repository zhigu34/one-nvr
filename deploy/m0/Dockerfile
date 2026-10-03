FROM python:3.12-slim
RUN apt-get update && apt-get install -y --no-install-recommends ffmpeg openssl \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY requirements.txt .
RUN pip install --no-cache-dir --require-hashes -r requirements.txt
COPY common.py store.py probe.py bootstrap.py ./
CMD ["python", "probe.py"]
