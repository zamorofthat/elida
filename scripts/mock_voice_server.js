#!/usr/bin/env node
/**
 * Mock Voice Server for ELIDA Testing
 *
 * Simulates OpenAI Realtime API for testing WebSocket voice sessions
 * without requiring API access or paid services.
 *
 * Usage:
 *   npm install ws
 *   node scripts/mock_voice_server.js
 *
 * Then configure ELIDA:
 *   backends:
 *     mock:
 *       url: "ws://localhost:11434"
 *       default: true
 *   websocket:
 *     enabled: true
 *     voice_sessions:
 *       enabled: true
 */

const WebSocket = require('ws');

const PORT = process.env.PORT || 11434;
const wss = new WebSocket.Server({ port: PORT });

console.log(`Mock Voice Server running on ws://localhost:${PORT}`);
console.log('');
console.log('Simulates OpenAI Realtime API for testing ELIDA voice sessions.');
console.log('');
console.log('Test commands to send:');
console.log('  {"type":"session.create","session":{"model":"gpt-4o-realtime"}}');
console.log('  {"type":"input_audio_buffer.commit"}');
console.log('  {"type":"conversation.item.create","item":{"content":[{"type":"text","text":"Hello"}]}}');
console.log('');

let sessionCounter = 0;
let responseCounter = 0;

wss.on('connection', (ws, req) => {
  sessionCounter++;
  const sessionId = `sess_mock_${sessionCounter.toString().padStart(3, '0')}`;

  console.log(`[${sessionId}] Client connected from ${req.socket.remoteAddress}`);

  // Send session.created on connect (simulates successful INVITE)
  const sessionCreated = {
    type: "session.created",
    session: {
      id: sessionId,
      model: "gpt-4o-realtime-preview",
      voice: "alloy",
      modalities: ["audio", "text"],
      instructions: "You are a helpful assistant."
    }
  };
  ws.send(JSON.stringify(sessionCreated));
  console.log(`[${sessionId}] Sent session.created`);

  ws.on('message', (data) => {
    // Handle binary data (audio chunks)
    if (Buffer.isBuffer(data) && !isValidJSON(data)) {
      console.log(`[${sessionId}] Received audio chunk: ${data.length} bytes`);
      return;
    }

    try {
      const msg = JSON.parse(data.toString());
      console.log(`[${sessionId}] Received: ${msg.type}`);

      switch (msg.type) {
        case "session.create":
        case "session.update":
          // Echo back session.updated
          ws.send(JSON.stringify({
            type: "session.updated",
            session: {
              id: sessionId,
              model: msg.session?.model || "gpt-4o-realtime-preview",
              voice: msg.session?.voice || "alloy"
            }
          }));
          break;

        case "input_audio_buffer.append":
          // Just acknowledge, audio is being buffered
          break;

        case "input_audio_buffer.commit":
          // Simulate user speech transcription
          responseCounter++;
          const userTranscript = getRandomUserMessage();

          // Send transcription
          ws.send(JSON.stringify({
            type: "conversation.item.input_audio_transcription.completed",
            item_id: `item_user_${responseCounter}`,
            transcript: userTranscript
          }));
          console.log(`[${sessionId}] User said: "${userTranscript}"`);

          // Simulate assistant thinking and responding
          setTimeout(() => {
            const assistantResponse = getAssistantResponse(userTranscript);

            // Send response start
            ws.send(JSON.stringify({
              type: "response.created",
              response: { id: `resp_${responseCounter}`, status: "in_progress" }
            }));

            // Send audio transcript (what assistant is saying)
            ws.send(JSON.stringify({
              type: "response.audio_transcript.done",
              transcript: assistantResponse
            }));
            console.log(`[${sessionId}] Assistant said: "${assistantResponse}"`);

            // Send response done
            ws.send(JSON.stringify({
              type: "response.done",
              response: { id: `resp_${responseCounter}`, status: "completed" }
            }));
          }, 300 + Math.random() * 500);
          break;

        case "input_audio_buffer.clear":
          console.log(`[${sessionId}] Audio buffer cleared`);
          break;

        case "conversation.item.create":
          // Handle text input
          const textContent = msg.item?.content?.find(c => c.type === "text");
          if (textContent?.text) {
            responseCounter++;
            const response = getAssistantResponse(textContent.text);

            ws.send(JSON.stringify({
              type: "response.text.done",
              text: response
            }));
            console.log(`[${sessionId}] Text response: "${response}"`);

            ws.send(JSON.stringify({
              type: "response.done",
              response: { id: `resp_${responseCounter}`, status: "completed" }
            }));
          }
          break;

        case "response.create":
          // Client requesting a response
          console.log(`[${sessionId}] Response requested`);
          break;

        default:
          console.log(`[${sessionId}] Unhandled message type: ${msg.type}`);
      }
    } catch (e) {
      console.log(`[${sessionId}] Parse error:`, e.message);
    }
  });

  ws.on('close', (code, reason) => {
    console.log(`[${sessionId}] Client disconnected (code: ${code})`);
  });

  ws.on('error', (err) => {
    console.log(`[${sessionId}] Error:`, err.message);
  });
});

function isValidJSON(buffer) {
  try {
    JSON.parse(buffer.toString());
    return true;
  } catch {
    return false;
  }
}

const userMessages = [
  "Hello, how are you today?",
  "What's the weather like?",
  "Can you help me with something?",
  "Tell me a joke.",
  "What time is it?",
  "I have a question about programming.",
  "Can you explain quantum computing?",
  "What's the meaning of life?",
];

const assistantResponses = {
  default: "I understand. How can I help you further?",
  hello: "Hello! I'm doing great, thank you for asking. How can I assist you today?",
  weather: "I don't have access to real-time weather data, but I'd be happy to help with other questions!",
  help: "Of course! I'm here to help. What would you like assistance with?",
  joke: "Why do programmers prefer dark mode? Because light attracts bugs!",
  time: "I don't have access to the current time, but your device should show it!",
  programming: "I'd be happy to help with programming! What language or concept are you working with?",
  quantum: "Quantum computing uses quantum mechanics principles like superposition and entanglement to process information in ways classical computers cannot.",
  meaning: "That's a profound question! Many philosophers suggest the meaning of life is what we make of it.",
};

function getRandomUserMessage() {
  return userMessages[Math.floor(Math.random() * userMessages.length)];
}

function getAssistantResponse(userMessage) {
  const lower = userMessage.toLowerCase();

  if (lower.includes('hello') || lower.includes('hi')) return assistantResponses.hello;
  if (lower.includes('weather')) return assistantResponses.weather;
  if (lower.includes('help')) return assistantResponses.help;
  if (lower.includes('joke')) return assistantResponses.joke;
  if (lower.includes('time')) return assistantResponses.time;
  if (lower.includes('programming') || lower.includes('code')) return assistantResponses.programming;
  if (lower.includes('quantum')) return assistantResponses.quantum;
  if (lower.includes('meaning') || lower.includes('life')) return assistantResponses.meaning;

  return assistantResponses.default;
}

// Handle shutdown gracefully
process.on('SIGINT', () => {
  console.log('\nShutting down mock server...');
  wss.close(() => {
    console.log('Server closed.');
    process.exit(0);
  });
});
