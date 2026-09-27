package com.redxai.host
import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.compose.foundation.layout.*
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.unit.dp

class MainActivity:ComponentActivity(){override fun onCreate(savedInstanceState:Bundle?){super.onCreate(savedInstanceState);setContent{HostApp()}}}
@Composable fun HostApp(){
 var nodes by remember{mutableIntStateOf(0)}
 MaterialTheme(colorScheme=darkColorScheme(primary=Color(0xFF9E1028),secondary=Color(0xFF63338C),background=Color(0xFF090609))){
  Scaffold{p->Column(Modifier.padding(p).fillMaxSize().padding(16.dp),verticalArrangement=Arrangement.spacedBy(14.dp)){Text("Red-XAI Host",style=MaterialTheme.typography.headlineSmall);Text("Controller + mobile node");Text("Nodes: $nodes");Text("Android can run longer-lived foreground work than iOS, but the OS can still stop background processes. VPS/desktop nodes remain the durable hosting tier.");Button(onClick={nodes++}){Text("Add Node")}}}
 }
}
