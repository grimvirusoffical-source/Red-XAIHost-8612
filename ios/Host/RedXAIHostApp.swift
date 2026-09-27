import SwiftUI
import RedXAICore

@main struct RedXAIHostApp:App{var body:some Scene{WindowGroup{HostDashboard().preferredColorScheme(.dark)}}}

struct HostNode:Identifiable{let id=UUID();var name:String;var kind:String;var online:Bool;var cpu:Double}
@MainActor final class HostModel:ObservableObject{
    @Published var nodes:[HostNode]=[]
    @Published var projects=0
    @Published var traffic="0 B"
    @Published var status="Controller ready"
    func beginPairing(){nodes.append(.init(name:"Pending Node",kind:"Awaiting enrollment",online:false,cpu:0));status="Pairing placeholder created. Remote enrollment API is the next integration gate."}
}
struct HostDashboard:View{
    @StateObject private var model=HostModel()
    var body:some View{NavigationStack{ScrollView{VStack(spacing:16){
        HStack(spacing:12){RXBrandMark();VStack(alignment:.leading,spacing:2){Text("Red-XAI Host").font(.title2.bold());Text("Infrastructure control plane").font(.caption).foregroundStyle(.secondary)};Spacer();RXStatusPill("Controller")}
        HStack{Stat("Nodes","\(model.nodes.count)");Stat("Projects","\(model.projects)");Stat("Traffic",model.traffic)}
        GroupBox("iPhone role"){Text("Controller plus opportunistic node. iOS background limits mean this phone is never treated as an unrestricted 24/7 VPS.").frame(maxWidth:.infinity,alignment:.leading)}
        GroupBox("Nodes"){VStack{if model.nodes.isEmpty{ContentUnavailableView("No nodes paired",systemImage:"server.rack")}else{ForEach(model.nodes){n in HStack{Circle().fill(n.online ? .green:.secondary).frame(width:9,height:9);VStack(alignment:.leading){Text(n.name).bold();Text(n.kind).font(.caption).foregroundStyle(.secondary)};Spacer();Text("\(Int(n.cpu))% CPU").font(.caption.monospacedDigit())}}};Button("Add Node",systemImage:"plus"){model.beginPairing()}.buttonStyle(.borderedProminent).tint(RedXAITheme.blood)}}
        GroupBox("Status"){Text(model.status).frame(maxWidth:.infinity,alignment:.leading)}
    }.padding()}.background(RXPalette.background).navigationBarHidden(true)}}
}
struct Stat: View {
    let title: String
    let value: String
    init(_ title: String, _ value: String) {
        self.title = title
        self.value = value
    }
    var body: some View {
        VStack {
            Text(value).font(.title3.bold())
            Text(title).font(.caption).foregroundStyle(.secondary)
        }
        .frame(maxWidth: .infinity)
        .padding()
        .background(RXPalette.surface, in: RoundedRectangle(cornerRadius: 18, style: .continuous))\n        .overlay(RoundedRectangle(cornerRadius:18).stroke(RXPalette.border))
    }
}
