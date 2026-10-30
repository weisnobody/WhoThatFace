FROM python:3.11-slim

WORKDIR /opt/face-me-bro
COPY worker/requirements.lock /opt/face-me-bro/worker/requirements.lock
RUN python -m pip install --no-cache-dir --requirement worker/requirements.lock
COPY worker/insightface_worker.py /opt/face-me-bro/worker/insightface_worker.py
COPY scripts/setup_models.py /opt/face-me-bro/scripts/setup_models.py
ENV FACE_ME_BRO_MODEL_ROOT=/opt/face-me-bro/models
RUN python scripts/setup_models.py --model buffalo_l --root /opt/face-me-bro/models
ENTRYPOINT ["python", "worker/insightface_worker.py"]
