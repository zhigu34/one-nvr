FROM python:3.12-slim

ARG DEBIAN_MIRROR=http://deb.debian.org/debian
ARG DEBIAN_SECURITY_MIRROR=http://deb.debian.org/debian-security
ARG PYPI_INDEX_URL=https://pypi.org/simple

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1

# Keep the image's suite/signing keys, and support both Debian source formats.
RUN set -eu; \
    for file in /etc/apt/sources.list.d/debian.sources /etc/apt/sources.list; do \
      if [ -f "$file" ]; then \
        cp "$file" "$file.one-nvr-original"; \
        sed -i \
          -e "s|https\?://deb.debian.org/debian-security|${DEBIAN_SECURITY_MIRROR}|g" \
          -e "s|https\?://security.debian.org/debian-security|${DEBIAN_SECURITY_MIRROR}|g" \
          -e "s|https\?://deb.debian.org/debian|${DEBIAN_MIRROR}|g" "$file"; \
      fi; \
    done; \
    install_packages() { \
      apt-get -o Acquire::Retries=3 -o Acquire::http::Timeout=15 \
        -o Acquire::https::Timeout=15 -o Acquire::http::Pipeline-Depth=0 \
        -o APT::Update::Error-Mode=any update && \
      apt-get -o Acquire::Retries=3 -o Acquire::http::Timeout=15 \
        -o Acquire::https::Timeout=15 -o Acquire::http::Pipeline-Depth=0 \
        install -y --no-install-recommends ffmpeg openssl ca-certificates; \
    }; \
    if ! install_packages; then \
      echo 'Preferred Debian mirror failed; retrying original sources' >&2; \
      for file in /etc/apt/sources.list.d/debian.sources /etc/apt/sources.list; do \
        if [ -f "$file.one-nvr-original" ]; then cp "$file.one-nvr-original" "$file"; fi; \
      done; \
      rm -rf /var/lib/apt/lists/*; \
      install_packages; \
    fi; \
    rm -rf /var/lib/apt/lists/*; \
    rm -f /etc/apt/sources.list.one-nvr-original /etc/apt/sources.list.d/debian.sources.one-nvr-original

WORKDIR /app
COPY requirements.txt .
RUN python -m pip install --disable-pip-version-check --no-cache-dir \
      --only-binary=:all: --require-hashes --retries 3 --timeout 20 \
      --index-url "${PYPI_INDEX_URL}" -r requirements.txt \
    || python -m pip install --disable-pip-version-check --no-cache-dir \
      --only-binary=:all: --require-hashes --retries 3 --timeout 20 \
      --index-url https://pypi.org/simple -r requirements.txt
COPY common.py store.py probe.py bootstrap.py ./
COPY static/index.html ./static/index.html
CMD ["python", "probe.py"]
