const WebSocket = require('ws');
const { spawn } = require('child_process');
const path = require('path');

async function sleep(ms) {
    return new Promise(resolve => setTimeout(resolve, ms));
}

async function testClientServer() {
    console.log('Starting integration tests...');
    
    // Start Go server
    const serverPath = path.join(__dirname, '..', 'integration', 'server.go');
    const server = spawn('go', ['run', serverPath]);
    
    let serverOutput = '';
    server.stdout.on('data', (data) => {
        serverOutput += data.toString();
        console.log('Server:', data.toString().trim());
    });
    
    server.stderr.on('data', (data) => {
        console.error('Server error:', data.toString().trim());
    });
    
    // Wait for server to start
    await sleep(2000);
    
    try {
        // Test 1: Basic connection
        console.log('\nTest 1: Basic WebSocket connection');
        const ws1 = new WebSocket('ws://localhost:8080/ws');
        
        await new Promise((resolve, reject) => {
            ws1.on('open', () => {
                console.log('✓ WebSocket connection established');
                resolve();
            });
            ws1.on('error', reject);
            setTimeout(() => reject(new Error('Connection timeout')), 5000);
        });
        
        // Test 2: Send and receive messages
        console.log('\nTest 2: Send and receive messages');
        const testMessage = 'Hello from Node.js client!';
        
        await new Promise((resolve, reject) => {
            ws1.on('message', (data) => {
                const received = data.toString();
                console.log('✓ Received echo:', received);
                if (received === `Echo: ${testMessage}`) {
                    resolve();
                } else {
                    reject(new Error(`Expected "Echo: ${testMessage}", got "${received}"`));
                }
            });
            
            ws1.send(testMessage);
            setTimeout(() => reject(new Error('Message timeout')), 5000);
        });
        
        ws1.close();
        
        // Test 3: Multiple connections
        console.log('\nTest 3: Multiple concurrent connections');
        const connections = [];
        const promises = [];
        
        for (let i = 0; i < 3; i++) {
            const ws = new WebSocket('ws://localhost:8080/ws');
            connections.push(ws);
            
            promises.push(new Promise((resolve, reject) => {
                ws.on('open', () => {
                    console.log(`✓ Connection ${i + 1} established`);
                    ws.send(`Message from connection ${i + 1}`);
                });
                
                ws.on('message', (data) => {
                    console.log(`✓ Connection ${i + 1} received:`, data.toString());
                    ws.close();
                    resolve();
                });
                
                ws.on('error', reject);
                setTimeout(() => reject(new Error(`Connection ${i + 1} timeout`)), 5000);
            }));
        }
        
        await Promise.all(promises);
        
        console.log('\n✓ All integration tests passed!');
        
    } catch (error) {
        console.error('\n✗ Integration test failed:', error.message);
        process.exit(1);
    } finally {
        if (server && !server.killed) {
            server.kill('SIGTERM');
            // Wait for the process to actually exit
            await new Promise((resolve) => {
                server.on('exit', resolve);
                setTimeout(resolve, 2000); // Fallback timeout
            });
        }
    }
}

// Test Go client against Node.js server
async function testGoClientNodeServer() {
    console.log('\nStarting Go client vs Node.js server test...');
    
    // Start Node.js WebSocket server
    const wss = new WebSocket.Server({ port: 8081 });
    
    wss.on('connection', (ws) => {
        console.log('Node.js server: Client connected');
        
        ws.on('message', (data) => {
            const message = data.toString();
            console.log('Node.js server received:', message);
            ws.send(`Node echo: ${message}`);
        });
    });
    
    console.log('Node.js WebSocket server started on port 8081');
    
    // Wait for server to start
    await sleep(1000);
    
    try {
        // Start Go client
        const clientPath = path.join(__dirname, '..', 'integration', 'client.go');
        const client = spawn('go', ['run', clientPath]);
        
        let clientOutput = '';
        client.stdout.on('data', (data) => {
            clientOutput += data.toString();
            console.log('Go client:', data.toString().trim());
        });
        
        client.stderr.on('data', (data) => {
            console.error('Go client error:', data.toString().trim());
        });
        
        await new Promise((resolve, reject) => {
            client.on('close', (code) => {
                if (code === 0) {
                    console.log('✓ Go client test completed successfully');
                    resolve();
                } else {
                    reject(new Error(`Go client exited with code ${code}`));
                }
            });
            
            setTimeout(() => {
                client.kill();
                reject(new Error('Go client timeout'));
            }, 10000);
        });
        
    } catch (error) {
        console.error('✗ Go client test failed:', error.message);
        process.exit(1);
    } finally {
        wss.close();
        // Wait for server to close
        await sleep(500);
    }
}

async function runTests() {
    const timeout = setTimeout(() => {
        console.error('\n💥 Integration tests timed out after 30 seconds');
        process.exit(1);
    }, 30000);
    
    try {
        await testClientServer();
        await testGoClientNodeServer();
        console.log('\n🎉 All integration tests completed successfully!');
        clearTimeout(timeout);
        process.exit(0);
    } catch (error) {
        console.error('\n💥 Integration tests failed:', error.message);
        clearTimeout(timeout);
        process.exit(1);
    }
}

runTests();