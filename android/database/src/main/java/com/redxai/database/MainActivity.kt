package com.redxai.database
import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.compose.foundation.layout.*
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.unit.dp
import com.redxai.core.RedXAIValidator

class MainActivity:ComponentActivity(){
 override fun onCreate(savedInstanceState:Bundle?){super.onCreate(savedInstanceState);setContent{DatabaseApp()}}
}
@Composable fun DatabaseApp(){
 var text by remember{mutableStateOf("{Red-XAI}[1]{\n    {PInfo}[232]{\n        {PlayerName}[2] = [\"Player\"][2],\n    <[True,13,false]>}\n<[True,1,false,\"keyref:database-reference\"]>}\n")}
 var status by remember{mutableStateOf("Ready")}
 MaterialTheme(colorScheme=darkColorScheme(primary=Color(0xFF9E1028),secondary=Color(0xFF63338C),background=Color(0xFF090609))){
  Scaffold{p->Column(Modifier.padding(p).fillMaxSize().padding(16.dp)){Text("Red-XAI Database",style=MaterialTheme.typography.headlineSmall);Spacer(Modifier.height(12.dp));Button(onClick={val r=RedXAIValidator.validate(text);status=if(r.valid)"Valid" else r.diagnostics.joinToString(" • "){it.message}}){Text("Validate")};Text(status);OutlinedTextField(value=text,onValueChange={text=it},modifier=Modifier.fillMaxSize(),textStyle=LocalTextStyle.current.copy(color=Color.White))}}
 }
}
