document.addEventListener('DOMContentLoaded', () => {
    const status = document.getElementById('status');
    const dropZone = document.getElementById('drop-zone');
    const fileInput = document.getElementById('file-input');
    const sentFilesList = document.getElementById('sent-files-list');
    const receivedFilesList = document.getElementById('received-files-list');

    const url = `ws://${window.location.host}/ws${window.location.pathname}`;
    const ws = new WebSocket(url);

    ws.onopen = () => {
        status.textContent = 'Connected. Waiting for other user...';
    };

    let currentFileMetadata = null;

    ws.onmessage = async (event) => {
        if (typeof event.data === 'string') {
            
            const data = JSON.parse(event.data);

            if (data.type === 'status') {
                status.textContent = data.message;
                if (data.ready) {
                    dropZone.classList.remove('hidden');
                }
            } else if (data.type === 'file_metadata') {
                currentFileMetadata = data;
            }
        } else if (event.data instanceof Blob) {
            
            if (currentFileMetadata) {
                const blob = new Blob([event.data], { type: currentFileMetadata.fileType });
                const url = URL.createObjectURL(blob);
                const li = document.createElement('li');
                const a = document.createElement('a');
                a.href = url;
                a.download = currentFileMetadata.fileName;
                a.textContent = `${currentFileMetadata.fileName} (${currentFileMetadata.fileSize} bytes)`;
                li.appendChild(a);

                if (currentFileMetadata.isSender) {
                    sentFilesList.appendChild(li);
                } else {
                    receivedFilesList.appendChild(li);
                }
                currentFileMetadata = null; // Clear metadata after processing
            } else {
                console.warn('Received binary data without preceding file metadata.');
            }
        }
    };

    ws.onclose = () => {
        status.textContent = 'Disconnected from server.';
        dropZone.classList.add('hidden');
    };

    ws.onerror = (error) => {
        console.error('WebSocket Error:', error);
        status.textContent = 'Connection error.';
    };

    dropZone.addEventListener('click', () => fileInput.click());
    dropZone.addEventListener('dragover', (e) => e.preventDefault());
    dropZone.addEventListener('drop', (e) => {
        e.preventDefault();
        const file = e.dataTransfer.files[0];
        if (file) {
            sendFile(file);
        }
    });

    fileInput.addEventListener('change', (e) => {
        const file = e.target.files[0];
        if (file) {
            sendFile(file);
        }
    });

    function sendFile(file) {
        const reader = new FileReader();
        reader.onload = (e) => {
            const payload = e.target.result;
            const metadata = {
                type: 'file_metadata',
                fileName: file.name,
                fileType: file.type,
                fileSize: file.size,
            };
            ws.send(JSON.stringify(metadata));
            ws.send(payload);

            
            const li = document.createElement('li');
            const a = document.createElement('a');
            const url = URL.createObjectURL(new Blob([payload], { type: file.type }));
            a.href = url;
            a.download = file.name;
            a.textContent = `${file.name} (${file.size} bytes)`;
            li.appendChild(a);
            sentFilesList.appendChild(li);
        };
        reader.readAsArrayBuffer(file);
    }
});